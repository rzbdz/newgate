package cli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/gateway/health"
	"github.com/rzbdz/newgate/go/internal/platform/httpx"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/probe"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"github.com/rzbdz/newgate/go/internal/store"
)

func warnShellEnvConflict(toolID string) {
	a, ok := agents.Get(toolID)
	if !ok {
		return
	}
	var conflict []string
	for _, k := range append([]string{a.BaseURLEnv, a.AuthEnv}, a.UnsetEnv...) {
		if os.Getenv(k) != "" {
			conflict = append(conflict, k)
		}
	}
	if len(conflict) == 0 {
		return
	}
	fmt.Printf("\n提示：你的 shell 里已导出 %s\n", strings.Join(conflict, ", "))
	fmt.Println("  shim 会在子进程里覆盖它们，所以**不影响** newgate 工作，不用改你的 rc。")
	fmt.Println("  但如果哪天你 newgate shim off，这些变量会重新生效（回到原来的直连行为）——这正是想要的。")
}

func cmdProbe(only string, asJSON bool) int {
	quiet := asJSON // JSON 模式不打进度，免得污染输出

	opts := probe.Options{
		Only:        only,
		Timeout:     120 * time.Second,
		Concurrency: 8,
		WaitTick:    5 * time.Second,
	}
	if !quiet {
		opts.OnPlan = func(ts []probe.Target) {
			fmt.Fprintf(os.Stderr, "探测 %d 个目标（并发 %d，同一 provider/model 只打一次）\n",
				len(ts), opts.Concurrency)
			for _, t := range ts {
				fmt.Fprintf(os.Stderr, "  · %s\n", t)
			}
			fmt.Fprintln(os.Stderr)
		}
		opts.OnDone = func(t probe.Target, status int, lat time.Duration, err error, done, total int) {
			light := probe.Light(err == nil && status == 200, lat)
			msg := ""
			if err != nil {
				msg = "  " + firstLine(err.Error(), 60)
			}
			st := "  -"
			if status > 0 {
				st = fmt.Sprintf("%3d", status)
			}
			fmt.Fprintf(os.Stderr, "[%2d/%2d] %s %-42s %s %6dms%s\n",
				done, total, light, t, st, lat.Milliseconds(), msg)
		}
		opts.OnWaiting = func(inflight map[probe.Target]time.Duration) {
			var parts []string
			for t, d := range inflight {
				parts = append(parts, fmt.Sprintf("%s(%ds)", t, int(d.Seconds())))
			}
			sort.Strings(parts)
			fmt.Fprintf(os.Stderr, "        ⏳ 还在等: %s\n", strings.Join(parts, ", "))
		}
	}

	results, err := probe.Run(opts)
	if err != nil {
		return die(65, err.Error())
	}
	if asJSON {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(b))
		return 0
	}

	fmt.Printf("\n%-9s %-7s %-42s %-5s %8s  %s\n",
		"PROFILE", "ROLE", "PROVIDER/MODEL", "状态", "延迟", "错误")
	fmt.Println(strings.Repeat("─", 108))
	lastProfile := ""
	for _, r := range results {
		p := r.Profile
		if p == lastProfile {
			p = ""
		} else {
			lastProfile = r.Profile
		}
		status := "  -"
		if r.Status > 0 {
			status = fmt.Sprintf("%3d", r.Status)
		}
		lat := ""
		if r.Latency > 0 {
			lat = fmt.Sprintf("%6dms", r.Latency.Milliseconds())
		}
		errMsg := r.Err
		if len(errMsg) > 44 {
			errMsg = errMsg[:44] + "…"
		}
		fmt.Printf("%-9s %-7s %-42s %s %s %8s  %s\n",
			p, r.Role, r.Provider+"/"+r.Model, r.Light(), status, lat, errMsg)
	}

	sums := probe.Summarize(results)
	fmt.Println()
	var healthy []string
	for _, s := range sums {
		light := "🟢"
		if s.Bad > 0 && s.OK > 0 {
			light = "🟡"
		} else if s.OK == 0 {
			light = "🔴"
		}
		if s.Bad == 0 && s.OK > 0 {
			healthy = append(healthy, fmt.Sprintf("%s(%dms)", s.Profile, s.AvgMs))
		}
		fmt.Printf("%s %-9s %d 通 / %d 挂", light, s.Profile, s.OK, s.Bad)
		if s.OK > 0 {
			fmt.Printf("   平均 %dms", s.AvgMs)
		}
		fmt.Println()
	}

	st := store.LoadState()
	fmt.Printf("\n当前 profile: %s", st.DefaultProfile)
	fmt.Printf("   chain: profile priority order (newgate tier <name>)")
	fmt.Println()

	// 当前 profile 有挂的就给出建议
	for _, s := range sums {
		if s.Profile == st.DefaultProfile && s.Bad > 0 {
			fmt.Printf("\n⚠ 当前 profile %s 有 %d 个档位不可用。", s.Profile, s.Bad)
			if len(healthy) > 0 {
				fmt.Printf("全绿的：%s\n", strings.Join(healthy, ", "))
				fmt.Printf("  newgate --set-profile %s\n", strings.SplitN(healthy[0], "(", 2)[0])
			} else {
				fmt.Println("没有全绿的 profile。")
			}
		}
	}
	return 0
}

func cmdDoctor() int {
	bad := 0
	fmt.Println("== 配置 ==")
	for _, p := range []string{paths.ProvidersFile(), paths.StateFile(), paths.Mappings()} {
		if _, err := os.Stat(p); err != nil {
			fmt.Printf("  ✗ %s 不存在（newgate init）\n", p)
			bad++
		} else {
			fmt.Printf("  ✓ %s\n", p)
		}
	}

	fmt.Println("\n== profile / provider ==")
	probs := store.Validate()
	if len(probs) == 0 {
		fmt.Println("  ✓ 全部 profile 的 provider 与 key 都齐")
	}
	for _, p := range probs {
		fmt.Printf("  ✗ %s\n", p)
		bad++
	}

	fmt.Println("\n== 出站代理环境变量 ==")
	bad += checkProxyEnv()

	fmt.Println("\n== 代理 ==")
	st := store.LoadState()
	if i := daemon.Running(); i != nil {
		if pingProxy(i.Port) {
			fmt.Printf("  ✓ pid %d 在 127.0.0.1:%d 响应\n", i.PID, i.Port)
		} else {
			fmt.Printf("  ✗ pid %d 活着但端口 %d 不响应\n", i.PID, i.Port)
			bad++
		}
	} else {
		fmt.Printf("  - 未运行（端口 %d）\n", st.Port)
	}

	fmt.Println("\n== 目标文件 ==")
	for _, t := range paths.TargetFiles() {
		b, err := ioutil.ReadFile(t)
		if err != nil {
			fmt.Printf("  - %s 不存在\n", t)
			continue
		}
		state := "未接管"
		if injection.IsTakenOver(t) {
			state = "已接管"
		}
		fmt.Printf("  ✓ %s (%d 字节, %s)\n", t, len(b), state)
	}

	fmt.Println("\n== 备份 ==")
	orig := paths.BackupDir() + "/original"
	if ents, err := ioutil.ReadDir(orig); err == nil && len(ents) > 0 {
		for _, e := range ents {
			fmt.Printf("  ✓ %s/%s（newgate stop 会还原它）\n", orig, e.Name())
		}
	} else {
		fmt.Println("  - 还没有备份（没接管过）")
	}

	fmt.Printf("\n%d 个问题\n", bad)
	if bad > 0 {
		return 1
	}
	return 0
}

func cmdLogs(n int, follow bool) int {
	if follow {
		c := exec.Command("tail", "-n", strconv.Itoa(n), "-f", paths.LogFile())
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		_ = c.Run()
		return 0
	}
	b, err := ioutil.ReadFile(paths.LogFile())
	if err != nil {
		return die(69, "读不到日志 "+paths.LogFile()+"："+err.Error())
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	fmt.Println(strings.Join(lines, "\n"))
	return 0
}

func cmdAllLogs() int {
	line := func(t string) { fmt.Printf("\n===== %s =====\n", t) }

	line("版本与环境")
	fmt.Println(VersionLine())
	fmt.Printf("配置目录 %s\n", paths.Config())
	fmt.Printf("日志     %s\n", paths.LogFile())
	for _, k := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"no_proxy", "NO_PROXY", "NEWGATE_HOME", "NEWGATE_TARGET_DIR", "NEWGATE_DUMP"} {
		if v := os.Getenv(k); v != "" {
			fmt.Printf("env      %s=%s\n", k, v)
		}
	}

	line("状态")
	cmdStatus()

	line("providers.json（密钥脱敏）")
	if provs, err := store.LoadProviders(); err == nil {
		for _, n := range sortedKeys(provs.Providers) {
			p := provs.Providers[n]
			k := "(空)"
			if v := p.Key(); v != "" {
				if len(v) > 10 {
					k = v[:7] + "…" + fmt.Sprint(len(v)) + "字符"
				} else {
					k = "(过短)"
				}
			}
			fmt.Printf("  %-14s %-45s protocol=%-10s key=%s\n", n, p.BaseURL, p.Protocol, k)
		}
	}

	line("所有 profile 的绑定")
	names, _ := store.ListProfiles()
	st := store.LoadState()
	for _, n := range names {
		mark := " "
		if n == st.DefaultProfile {
			mark = "*"
		}
		fb := ""
		if n == st.DefaultProfile {
			fb = "  (备用)"
		}
		fmt.Printf(" %s %s%s\n", mark, n, fb)
		if pr, err := store.LoadProfile(n); err == nil {
			for _, role := range domain.Roles {
				if b, ok := pr.Resolve(role); ok {
					fmt.Printf("      %-8s %s/%s\n", role, b.Provider, b.Model)
				}
			}
		}
	}

	line("接管后的目标文件（newgate 相关片段）")
	for _, t := range paths.TargetFiles() {
		b, err := ioutil.ReadFile(t)
		if err != nil {
			fmt.Printf("  %s : %v\n", t, err)
			continue
		}
		fmt.Printf("  --- %s (%d 字节) ---\n", t, len(b))
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "newgate") {
				fmt.Printf("    %s\n", strings.TrimSpace(ln))
			}
		}
	}

	line("错误证据文件")
	dumpDir := filepath.Join(paths.Config(), "dump")
	ents, err := ioutil.ReadDir(dumpDir)
	if err != nil || len(ents) == 0 {
		fmt.Println("  （无。上游报 4xx/5xx 时会自动生成）")
	} else {
		for _, e := range ents {
			fmt.Printf("  %s  %d 字节\n", filepath.Join(dumpDir, e.Name()), e.Size())
		}
		fmt.Println("\n  看「我们发出的」和「客户端发来的」差在哪：")
		fmt.Printf("    diff <(jq -S . %s/err-*.client-sent.json) \\\n", dumpDir)
		fmt.Printf("         <(jq -S . %s/err-*.we-sent.json)\n", dumpDir)
	}

	line("日志全文")
	b, err := ioutil.ReadFile(paths.LogFile())
	if err != nil {
		fmt.Printf("  读不到: %v\n", err)
	} else {
		fmt.Print(string(b))
	}
	return 0
}

func cmdInit(force bool) int {
	created, err := store.Init(force)
	if err != nil {
		return die(70, err.Error())
	}
	if len(created) == 0 {
		fmt.Println("配置已存在，无需初始化（--force 可覆盖）")
	} else {
		for _, c := range created {
			fmt.Println("创建 " + c)
		}
	}
	fmt.Printf("\n下一步：把上游 key 填进 %s\n", paths.ProvidersFile())
	fmt.Println("默认写入的是占位符，必须改成你自己的 provider / endpoint / 模型名。")
	fmt.Println("key 建议走环境变量（不落盘）：NEWGATE_KEY_<PROVIDER 大写，- 换 _>")
	return 0
}

func sortedKeys(m map[string]domain.Provider) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstLine(s string, n int) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func checkProxyEnv() int {
	type ev struct{ name, val string }
	var set []ev
	for _, n := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"all_proxy", "ALL_PROXY"} {
		if v := os.Getenv(n); v != "" {
			set = append(set, ev{n, v})
		}
	}
	noProxy := os.Getenv("no_proxy") + "," + os.Getenv("NO_PROXY")

	if len(set) == 0 {
		fmt.Println("  ✓ 没设出站代理")
		return 0
	}
	for _, e := range set {
		fmt.Printf("  · %s=%s\n", e.name, e.val)
	}

	covered := 0
	for _, want := range []string{"127.0.0.1", "localhost"} {
		if strings.Contains(noProxy, want) {
			covered++
		}
	}
	if covered == 2 {
		fmt.Printf("  ✓ NO_PROXY 已包含 127.0.0.1 和 localhost\n")
		return 0
	}

	fmt.Printf("  ✗ NO_PROXY=%q 没有同时包含 127.0.0.1 和 localhost\n",
		strings.Trim(noProxy, ","))
	fmt.Println("     ↳ 这会让 opencode 把 http://127.0.0.1:8899 的请求也发给出站代理，")
	fmt.Println("       代理连不上 127.0.0.1 就回 502 Bad Gateway——而 newgate 日志里")
	fmt.Println("       一条请求都不会有，因为请求根本没到我们这。")
	fmt.Println("     修法（加到 shell rc 里，然后重开 opencode）：")
	fmt.Println("       export NO_PROXY=\"127.0.0.1,localhost,::1,$NO_PROXY\"")
	fmt.Println("       export no_proxy=\"$NO_PROXY\"")
	return 1
}

func cmdStatus() int {
	st := store.LoadState()
	fmt.Printf("版本      %s  (构建于 %s)\n", Version, pretty(BuildTime))
	fmt.Printf("profile   %s\n", st.DefaultProfile)
	flags := ""
	if st.DebugActive() {
		flags += " debug=on"
	}
	if !st.RepairEnabled() {
		flags += " schema-repair=off"
	}
	if st.Debug && !st.DebugActive() {
		flags += " debug=已过期(自动关)"
	} else if st.DebugActive() && st.DebugUntil != "" {
		flags += " 到 " + st.DebugUntil
	}
	if flags != "" {
		fmt.Printf("开关     %s\n", flags)
	}

	if i := daemon.Running(); i != nil {
		alive := "✓ 可达"
		if !pingProxy(i.Port) {
			if httpx.TCPAlive("127.0.0.1", i.Port, time.Second) {
				alive = "⚠ 端口在听但 HTTP 探活失败（出站代理劫持？newgate doctor）"
			} else {
				alive = "✗ 端口不响应"
			}
		}
		fmt.Printf("代理      运行中 pid=%d 127.0.0.1:%d  %s\n", i.PID, i.Port, alive)
	} else {
		fmt.Printf("代理      未运行  (newgate start)\n")
	}

	pr, err := store.LoadProfile(st.DefaultProfile)
	if err != nil {
		fmt.Printf("⚠ profile 读不出: %v\n", err)
		return 0
	}
	provs, _ := store.LoadProviders()
	fmt.Println("\n档位绑定:")
	for _, role := range domain.Roles {
		b, ok := pr.Resolve(role)
		if !ok {
			fmt.Printf("  %-8s 未绑定\n", role)
			continue
		}
		note := ""
		if provs != nil {
			if p, ok2 := provs.Providers[b.Provider]; !ok2 {
				note = "  ⚠ provider 未定义"
			} else if p.Key() == "" {
				note = "  ⚠ 缺 api_key"
			}
		}
		fmt.Printf("  %-8s %s/%s%s\n", role, b.Provider, b.Model, note)
	}

	if snap, err := store.Load(); err == nil {
		fmt.Println("\nfallback chain (heavy tier; full detail: newgate tier heavy):")
		steps, _ := resolve.BuildChain("heavy", snap.Profiles, snap.Providers, resolve.Opts{
			Active: st.DefaultProfile, Available: health.Default.Available,
			MaxSteps: st.Chain.Attempts()})
		for i, stp := range steps {
			arrow := "  ↓ "
			if i == 0 {
				arrow = "  → "
			}
			fmt.Printf("%s%s: %s\n", arrow, stp.Profile, stp.Binding)
		}
		if len(steps) == 0 {
			fmt.Println("  ⚠ 无可用候选，跑 newgate role heavy 看原因")
		}
	}

	fmt.Println("\n接管状态:")
	for _, t := range paths.TargetFiles() {
		if _, err := os.Stat(t); os.IsNotExist(err) {
			fmt.Printf("  %-55s 文件不存在\n", t)
			continue
		}
		s := "未接管"
		if injection.IsTakenOver(t) {
			s = "✓ 已接管"
		}
		fmt.Printf("  %-55s %s\n", t, s)
	}
	return 0
}
