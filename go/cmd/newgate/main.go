package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/internal/config"
	"github.com/rzbdz/newgate/go/internal/daemon"
	"github.com/rzbdz/newgate/go/internal/health"
	"github.com/rzbdz/newgate/go/internal/httpx"
	"github.com/rzbdz/newgate/go/internal/logx"
	"github.com/rzbdz/newgate/go/internal/paths"
	"github.com/rzbdz/newgate/go/internal/probe"
	"github.com/rzbdz/newgate/go/internal/proxy"
	"github.com/rzbdz/newgate/go/internal/shim"
	"github.com/rzbdz/newgate/go/internal/takeover"
	"github.com/rzbdz/newgate/go/internal/tools"
	"github.com/rzbdz/newgate/go/internal/tui"
)

var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)

// versionLine 一行版本信息，带构建时间，方便一眼看出装的是不是最新的。
func versionLine() string {
	return fmt.Sprintf("newgate %s\n  构建于 %s\n  提交于 %s",
		version, prettyTS(buildTime), prettyTS(commitTime))
}

// prettyTS ldflags 传不了空格，所以用下划线占位，展示时换回来。
func prettyTS(s string) string { return strings.ReplaceAll(s, "_", " ") }

const usage = `newgate ` + "" + ` — AI 工具配置的语义命名层 (M0 PoC)

用法:
  newgate start [--force]      启动代理 + 接管 opencode / oh-my-openagent 配置
  newgate stop                 停代理 + 还原配置（逃生舱）
  newgate restart              重启
  newgate status               当前状态
  newgate --set-profile <名>   切换档位绑定（立刻生效，不用重启 opencode）
  newgate --set-fallback <名>  设置备用 profile（主上游挂了自动转移；off 关闭）
  newgate shim install [工具]   装 PATH shim（默认 claude），之后直接敲 claude 就走 newgate
  newgate shim off / on [工具]  临时停用 / 恢复（只动符号链接，不碰你的 rc）
  newgate shim uninstall        彻底移除：删链接 + 删 rc 里的 PATH 行
  newgate shim status
  newgate role <档位>          看这个档位的 fallback 链：每个候选为什么选中/跳过
  newgate probe [名]           给每个 profile 的每个档位打一次真实请求，出健康报告
  newgate profiles             列出所有 profile
  newgate tui                  menuconfig 风格配置界面
  newgate init [--force]       铺开默认配置到 ~/.config/newgate/
  newgate logs [N] [-f]        看代理日志（每条请求走了哪个 provider/model）
  newgate alllogs              完整诊断包：状态+绑定+日志全文+错误证据文件
  newgate debug on|off         开/关全量请求日志（记完整请求体，密钥脱敏）
  newgate schema-repair on|off 开/关 tool schema 修补（默认开，见下）
  newgate doctor               体检
  newgate version

档位 (role): heavy / mid / light / vision
配置目录:    ~/.config/newgate/
  providers.json     上游账号（endpoint + key，0600）
  mappings/*.json    每个文件一个 profile，文件名就是 profile 名
  state.json         当前激活的 profile
pid / lock:  ~/.newgate.pid  ~/.newgate.lock

schema-repair: 给缺 required 的 tool schema 补上 "required": []。
  按 JSON Schema 规范这与不写 required 语义完全等价，是无操作修补。
  DeepSeek 上游把「缺失」当 null 校验，会报 400 "null is not of type array"；
  Claude 上游宽容。只重写 tools 一个字段，messages 仍逐字节不动。
`

func main() {
	// argv0 分发：如果我们是通过 PATH shim 被当成某个工具调用的，就进 wrapper 模式
	if t, ok := tools.Get(wrapperName(os.Args[0])); ok {
		runAsWrapper(t, os.Args)
		return
	}

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage)
		os.Exit(0)
	}

	// --set-profile 是动作型选项，可放任意位置
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--set-profile" || strings.HasPrefix(a, "--set-profile=") {
			var name string
			if strings.Contains(a, "=") {
				name = strings.SplitN(a, "=", 2)[1]
			} else if i+1 < len(args) {
				name = args[i+1]
			}
			if name == "" {
				die(64, "--set-profile 需要一个 profile 名")
			}
			cmdSetProfile(name)
			return
		}
		if a == "--set-fallback" || strings.HasPrefix(a, "--set-fallback=") {
			var name string
			if strings.Contains(a, "=") {
				name = strings.SplitN(a, "=", 2)[1]
			} else if i+1 < len(args) {
				name = args[i+1]
			}
			if name == "" {
				die(64, "--set-fallback 需要一个 profile 名（或 off 关闭）")
			}
			if name == "off" || name == "none" {
				name = ""
			}
			if err := config.SetFallbackProfile(name); err != nil {
				die(65, err.Error())
			}
			if name == "" {
				fmt.Println("✓ 已关闭自动转移")
			} else {
				fmt.Printf("✓ 备用 profile → %s（主上游报 5xx/429/连不上时自动转过去）\n", name)
			}
			return
		}
	}

	switch args[0] {
	case "start":
		cmdStart(contains(args, "--force"))
	case "stop":
		cmdStop()
	case "restart":
		cmdStop()
		time.Sleep(200 * time.Millisecond)
		cmdStart(contains(args, "--force"))
	case "status":
		cmdStatus()
	case "profiles", "ls":
		cmdProfiles()
	case "shim":
		sub := ""
		if len(args) > 1 {
			sub = args[1]
		}
		which := "claude"
		if len(args) > 2 {
			which = args[2]
		}
		cmdShim(sub, which)
	case "role", "roles":
		which := ""
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			which = args[1]
		}
		cmdRole(which)
	case "probe":
		only := ""
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			only = args[1]
		}
		cmdProbe(only, contains(args, "--json"))
	case "init":
		cmdInit(contains(args, "--force"))
	case "tui", "menuconfig":
		if err := tui.Run(); err != nil {
			die(70, err.Error())
		}
	case "doctor":
		cmdDoctor()
	case "alllogs", "all-logs":
		cmdAllLogs()
	case "schema-repair", "schema_repair":
		on := len(args) > 1 && (args[1] == "on" || args[1] == "1" || args[1] == "true")
		if err := config.SetSchemaRepair(on); err != nil {
			die(70, err.Error())
		}
		if on {
			fmt.Println("✓ schema 修补已开（给缺 required 的 tool 补 \"required\": []）")
		} else {
			fmt.Println("✓ schema 修补已关：tools 完全按原样透传")
		}
		if daemon.Running() != nil {
			fmt.Println("  需要 newgate restart 生效")
		}
	case "debug":
		on := len(args) > 1 && (args[1] == "on" || args[1] == "1" || args[1] == "true")
		ttl := 30 * time.Minute
		if on {
			for i := range args {
				if m, e := strconv.Atoi(args[i]); e == nil && m > 0 {
					ttl = time.Duration(m) * time.Minute
				}
			}
			if contains(args, "--forever") {
				ttl = 0
			}
		}
		if err := config.SetDebug(on, ttl); err != nil {
			die(70, err.Error())
		}
		if on {
			if ttl > 0 {
				fmt.Printf("✓ debug 已开，%v 后自动关闭（避免写满磁盘）\n", ttl)
				fmt.Println("  想一直开：newgate debug on --forever")
			} else {
				fmt.Println("✓ debug 已开（不自动关闭，记得手动 newgate debug off）")
			}
			fmt.Printf("  日志 %s，16MB 轮转保留 4 份\n", paths.LogFile())
		} else {
			fmt.Println("✓ debug 已关")
		}
	case "logs", "log":
		n := 40
		if len(args) > 1 {
			if v, e := strconv.Atoi(args[1]); e == nil {
				n = v
			}
		}
		cmdLogs(n, contains(args, "-f") || contains(args, "--follow"))
	case "version", "--version", "-v":
		fmt.Println(versionLine())
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "__serve": // 内部：守护进程入口
		port := config.ProxyPort
		for i := range args {
			if args[i] == "--port" && i+1 < len(args) {
				port, _ = strconv.Atoi(args[i+1])
			}
		}
		serve(port)
	default:
		die(64, fmt.Sprintf("未知命令 %q（newgate --help）", args[0]))
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func die(code int, msg string) {
	fmt.Fprintf(os.Stderr, "newgate: %s\n", msg)
	os.Exit(code)
}

// ---------- 守护进程 ----------

func serve(port int) {
	// 日志走轮转写入器：debug 模式单条能记 8KB+，没上限会把磁盘写满
	rot, rerr := logx.New(paths.LogFile(), 16<<20, 3) // 16MB × 4 份
	var lg *log.Logger
	if rerr != nil {
		lg = log.New(os.Stdout, "", log.LstdFlags)
		lg.Printf("日志轮转初始化失败，退回 stdout: %v", rerr)
	} else {
		defer rot.Close()
		lg = log.New(rot, "", log.LstdFlags)
	}
	if err := daemon.AcquireLock(); err != nil {
		lg.Printf("抢锁失败: %v", err)
		os.Exit(69)
	}
	defer daemon.RemoveLock()

	srv := proxy.New(port, lg)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sig
		lg.Printf("收到 %v，退出", s)
		srv.Shutdown()
		daemon.RemoveLock()
		daemon.RemovePid()
		os.Exit(0)
	}()

	lg.Printf("newgate %s (构建于 %s) 启动，profile=%s",
		version, prettyTS(buildTime), config.LoadState().ActiveProfile)
	if err := srv.Start(); err != nil {
		lg.Printf("代理退出: %v", err)
		os.Exit(70)
	}
}

// ---------- 命令 ----------

func cmdInit(force bool) {
	created, err := config.Init(force)
	if err != nil {
		die(70, err.Error())
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
}

// activeProfileProblems 只检查当前 profile 用到的 provider，
// 别因为某个用不到的 profile 缺 key 就挡住启动。
func activeProfileProblems(st *config.State) []string {
	pr, err := config.LoadProfile(st.ActiveProfile)
	if err != nil {
		return []string{fmt.Sprintf("profile %q 读不出: %v", st.ActiveProfile, err)}
	}
	provs, err := config.LoadProviders()
	if err != nil {
		return []string{"providers.json 读不出: " + err.Error()}
	}
	seen := map[string]bool{}
	var out []string
	for _, role := range config.Roles {
		b, ok := pr.Resolve(role)
		if !ok {
			out = append(out, fmt.Sprintf("档位 %s 未绑定", role))
			continue
		}
		if seen[b.Provider] {
			continue
		}
		seen[b.Provider] = true
		p, ok := provs.Providers[b.Provider]
		if !ok {
			out = append(out, fmt.Sprintf("provider %q 未定义（档位 %s 用到它）", b.Provider, role))
			continue
		}
		if p.Key() == "" {
			hint := "providers.json 里填 api_key"
			if p.APIKeyEnv != "" {
				hint = "设环境变量 " + p.APIKeyEnv + " 或在 providers.json 里填 api_key"
			}
			out = append(out, fmt.Sprintf("provider %q 没有 key → %s", b.Provider, hint))
		}
	}
	return out
}

func cmdStart(force bool) {
	if _, err := os.Stat(paths.ProvidersFile()); os.IsNotExist(err) {
		fmt.Println("首次运行，先初始化配置...")
		if _, err := config.Init(false); err != nil {
			die(70, err.Error())
		}
	}
	if i := daemon.Running(); i != nil {
		fmt.Printf("已在运行 (pid %d, 端口 %d)\n", i.PID, i.Port)
		return
	}

	st := config.LoadState()

	// 没 key 就别接管——接管了每个请求都是 502，而且用户的配置已经被改了
	if probs := activeProfileProblems(st); len(probs) > 0 && !force {
		fmt.Fprintf(os.Stderr, "newgate: 当前 profile %q 还不能用，拒绝接管配置：\n", st.ActiveProfile)
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "  ✗ %s\n", p)
		}
		fmt.Fprintf(os.Stderr, "\n修法（二选一）：\n")
		fmt.Fprintf(os.Stderr, "  1. 把 key 填进 %s\n", paths.ProvidersFile())
		fmt.Fprintf(os.Stderr, "  2. export NEWGATE_KEY_UPSTREAM_A=sk-...（等对应的环境变量）\n")
		fmt.Fprintf(os.Stderr, "\n然后 newgate doctor 确认，再 newgate start。\n")
		fmt.Fprintf(os.Stderr, "（要强行接管：newgate start --force）\n")
		os.Exit(65)
	}

	info, err := daemon.Spawn(st.Port)
	if err != nil {
		die(70, "启动守护进程失败: "+err.Error())
	}

	// 等代理真的起来
	ok := false
	for k := 0; k < 50; k++ {
		if pingProxy(st.Port) {
			ok = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !ok {
		// 区分「进程没起来」和「起来了但我们连不上」——后者绝不能杀进程
		if httpx.TCPAlive("127.0.0.1", st.Port, time.Second) {
			fmt.Fprintf(os.Stderr,
				"newgate: 端口 %d 在听，但 HTTP 探活失败。代理进程保留。\n"+
					"  最常见原因：出站代理把 loopback 请求劫走了。跑 newgate doctor 看看。\n",
				st.Port)
		} else {
			fmt.Fprintf(os.Stderr, "newgate: 代理没起来（端口 %d 没在听），看日志 %s\n",
				st.Port, paths.LogFile())
			_, _ = daemon.Stop()
			os.Exit(70)
		}
	}
	fmt.Printf("✓ 代理已启动  pid=%d  127.0.0.1:%d  profile=%s\n",
		info.PID, st.Port, st.ActiveProfile)

	reps, err := takeover.ApplyAll(st.Port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
	}
	for _, r := range reps {
		if r.Skipped != "" {
			fmt.Printf("- %s  (跳过: %s)\n", r.File, r.Skipped)
			continue
		}
		fmt.Printf("✓ 接管 %s\n", r.File)
		shown := 0
		for _, rw := range r.Rewrites {
			if shown < 6 {
				fmt.Printf("    %s\n", rw)
				shown++
			}
		}
		if len(r.Rewrites) > shown {
			fmt.Printf("    ... 共 %d 处改写\n", len(r.Rewrites))
		}
		if r.Fuzzy > 0 {
			fmt.Printf("    ⚠ 其中 %d 处没命中精确规则，归到了中档，建议核对\n", r.Fuzzy)
		}
	}
	st.TakenOver = true
	_ = config.SaveState(st)
	fmt.Printf("\n新开的 opencode 会走 newgate。切换：newgate --set-profile ds\n")
	fmt.Printf("原配置备份在 %s/original/\n", paths.BackupDir())
}

func cmdStop() {
	restored, err := takeover.RestoreAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
	}
	for _, f := range restored {
		fmt.Printf("✓ 已还原 %s\n", f)
	}
	pid, err := daemon.Stop()
	if err != nil {
		die(70, err.Error())
	}
	if pid > 0 {
		fmt.Printf("✓ 代理已停 (pid %d)\n", pid)
	} else {
		fmt.Println("代理本来没在跑")
	}
	st := config.LoadState()
	st.TakenOver = false
	_ = config.SaveState(st)
}

func cmdStatus() {
	st := config.LoadState()
	fmt.Printf("版本      %s  (构建于 %s)\n", version, prettyTS(buildTime))
	fmt.Printf("profile   %s\n", st.ActiveProfile)
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

	pr, err := config.LoadProfile(st.ActiveProfile)
	if err != nil {
		fmt.Printf("⚠ profile 读不出: %v\n", err)
		return
	}
	provs, _ := config.LoadProviders()
	fmt.Println("\n档位绑定:")
	for _, role := range config.Roles {
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

	if profiles, provs, _, err := config.LoadChainInputs(); err == nil {
		fmt.Println("\nfallback 链（heavy 档位示例，完整看 newgate role）:")
		steps, _ := config.BuildChain("heavy", profiles, provs, config.ChainOpts{
			Active: st.ActiveProfile, Available: health.Default.Available,
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
		if takeover.IsTakenOver(t) {
			s = "✓ 已接管"
		}
		fmt.Printf("  %-55s %s\n", t, s)
	}
}

func cmdProbe(only string, asJSON bool) {
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
		die(65, err.Error())
	}
	if asJSON {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(b))
		return
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

	st := config.LoadState()
	fmt.Printf("\n当前 profile: %s", st.ActiveProfile)
	if st.FallbackProfile != "" {
		fmt.Printf("   备用: %s", st.FallbackProfile)
	} else {
		fmt.Printf("   备用: 未设置（newgate --set-fallback ds）")
	}
	fmt.Println()

	// 当前 profile 有挂的就给出建议
	for _, s := range sums {
		if s.Profile == st.ActiveProfile && s.Bad > 0 {
			fmt.Printf("\n⚠ 当前 profile %s 有 %d 个档位不可用。", s.Profile, s.Bad)
			if len(healthy) > 0 {
				fmt.Printf("全绿的：%s\n", strings.Join(healthy, ", "))
				fmt.Printf("  newgate --set-profile %s\n", strings.SplitN(healthy[0], "(", 2)[0])
			} else {
				fmt.Println("没有全绿的 profile。")
			}
		}
	}
}

// cmdRole 打印某个档位（或全部）的 fallback 链。
// 这是验证「链有没有按预期排」的主要手段，也是 docs/18 §10 说的
// 「一屏说清为什么是它、我该改什么」。
func cmdRole(which string) {
	profiles, provs, st, err := config.LoadChainInputs()
	if err != nil {
		die(65, err.Error())
	}
	roles := config.Roles
	if which != "" {
		roles = []string{which}
	}

	fmt.Printf("链头 %s", st.ActiveProfile)
	for _, p := range profiles {
		if p.Name == st.ActiveProfile && p.Pinned {
			fmt.Printf("  [pinned：链到此为止，不往下掉]")
		}
	}
	fmt.Printf("    maxAttempts=%d  totalBudget=%dms\n",
		st.Chain.Attempts(), st.Chain.Budget())

	for _, role := range roles {
		steps, skips := config.BuildChain(role, profiles, provs, config.ChainOpts{
			Active:    st.ActiveProfile,
			Available: health.Default.Available,
			MaxSteps:  st.Chain.Attempts(),
		})
		fmt.Printf("\n\033[1m%s\033[0m", role)
		if len(steps) > 0 {
			fmt.Printf(" → \033[36m%s\033[0m  (profile %s)",
				steps[0].Binding, steps[0].Profile)
		} else {
			fmt.Printf(" → \033[31m无可用候选\033[0m")
		}
		fmt.Println()
		for i, stp := range steps {
			mark := "  "
			if i == 0 {
				mark = "→ "
			}
			fmt.Printf("  %s%d. %-12s %-44s %s\n", mark, i+1, stp.Profile,
				stp.Binding.String(),
				map[bool]string{true: "选中", false: "备选"}[i == 0])
		}
		for _, sk := range skips {
			t := sk.Target
			if t == "" {
				t = "(整个 profile)"
			}
			fmt.Printf("     -  %-12s %-44s \033[2m%s\033[0m\n", sk.Profile, t, sk.Reason)
		}
	}

	fmt.Println("\nprofile 优先级（越小越靠前）:")
	sorted := append([]*config.Profile{}, profiles...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Prio() != sorted[j].Prio() {
			return sorted[i].Prio() < sorted[j].Prio()
		}
		return sorted[i].Name < sorted[j].Name
	})
	for _, p := range sorted {
		flags := ""
		if p.Pinned {
			flags += " pinned"
		}
		if p.Excluded {
			flags += " excluded"
		}
		head := " "
		if p.Name == st.ActiveProfile {
			head = "*"
		}
		fmt.Printf(" %s %3d  %-12s%-20s %s\n", head, p.Prio(), p.Name, flags, p.Description)
	}
}

func cmdShim(sub, which string) {
	switch sub {
	case "install":
		link, err := shim.Install(which)
		if err != nil {
			die(65, err.Error())
		}
		fmt.Printf("✓ shim %s → %s\n", link, "newgate")

		var touched []string
		for _, rc := range shim.RCFiles() {
			changed, err := shim.AddToRC(rc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ %s: %v\n", rc, err)
				continue
			}
			if changed {
				touched = append(touched, rc)
				fmt.Printf("✓ 已把 %s 前置到 PATH（写入 %s）\n", shim.Dir(), rc)
			} else {
				fmt.Printf("- %s 已有 PATH 行，未重复写\n", rc)
			}
		}
		if len(touched) > 0 {
			fmt.Printf("  原文件备份在 %s/rc/\n", paths.BackupDir())
		}
		if t, ok := tools.Get(which); ok {
			if real, err := t.FindReal(shim.Dir()); err == nil {
				fmt.Printf("\n真实 %s: %s\n", which, real)
			} else {
				fmt.Printf("\n⚠ 找不到真实的 %s：%v\n", which, err)
			}
		}
		if !shim.InPath() {
			fmt.Printf("\n\033[1m下一步：重新加载 shell 让 PATH 生效\033[0m\n")
			fmt.Printf("  exec $SHELL -l          # 或者 source ~/.zshrc\n")
			fmt.Printf("然后 `which claude` 应该指向 %s/claude\n", shim.Dir())
		}
		warnShellEnvConflict(which)

	case "off":
		if err := shim.Uninstall(which); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		}
		fmt.Printf("✓ 已移除 shim。%s 恢复直连（PATH 行留着，空目录无害）\n", which)
		fmt.Println("  新开的 shell 立刻生效；当前 shell 可能有 command 缓存，用 `hash -r`（bash）或 `rehash`（zsh）")

	case "on":
		link, err := shim.Install(which)
		if err != nil {
			die(65, err.Error())
		}
		fmt.Printf("✓ shim 已恢复 %s\n", link)

	case "uninstall":
		for _, n := range shim.Installed() {
			_ = shim.Uninstall(n)
			fmt.Printf("✓ 删除 shim %s\n", n)
		}
		for _, rc := range shim.RCFiles() {
			changed, err := shim.RemoveFromRC(rc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ %s: %v\n", rc, err)
				continue
			}
			if changed {
				fmt.Printf("✓ 已从 %s 移除 PATH 行\n", rc)
			}
		}
		fmt.Println("\n完全恢复原状。重开 shell 生效。")

	default:
		fmt.Printf("shim 目录   %s\n", shim.Dir())
		fmt.Printf("在 PATH 里  %v\n", shim.InPath())
		inst := shim.Installed()
		if len(inst) == 0 {
			fmt.Println("已装 shim   （无）")
		} else {
			fmt.Printf("已装 shim   %s\n", strings.Join(inst, ", "))
		}
		for _, rc := range shim.RCFiles() {
			fmt.Printf("  %-40s PATH 行: %v\n", rc, shim.HasBlock(rc))
		}
		for _, n := range inst {
			if t, ok := tools.Get(n); ok {
				if real, err := t.FindReal(shim.Dir()); err == nil {
					fmt.Printf("  真实 %-8s %s\n", n, real)
				}
			}
		}
		fmt.Println("\n用法: newgate shim install|off|on|uninstall|status [工具]")
	}
}

// warnShellEnvConflict shell 里已经导出的变量会盖过配置文件，但盖不过 shim。
// 这里只提示，不自动改用户的 rc。
func warnShellEnvConflict(toolID string) {
	t, ok := tools.Get(toolID)
	if !ok {
		return
	}
	var conflict []string
	for _, k := range append([]string{t.BaseURLEnv, t.AuthEnv}, t.UnsetEnv...) {
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

func cmdProfiles() {
	names, err := config.ListProfiles()
	if err != nil {
		die(65, "读 mappings 失败: "+err.Error())
	}
	st := config.LoadState()
	for _, n := range names {
		mark := " "
		if n == st.ActiveProfile {
			mark = "*"
		}
		desc := ""
		if pr, err := config.LoadProfile(n); err == nil {
			desc = pr.Description
		}
		fmt.Printf(" %s %-10s %s\n", mark, n, desc)
	}
}

func cmdSetProfile(name string) {
	if err := config.SetActiveProfile(name); err != nil {
		die(65, err.Error())
	}
	pr, _ := config.LoadProfile(name)
	fmt.Printf("✓ profile → %s", name)
	if pr != nil && pr.Description != "" {
		fmt.Printf("  (%s)", pr.Description)
	}
	fmt.Println()
	for _, role := range config.Roles {
		if b, ok := pr.Resolve(role); ok {
			fmt.Printf("    %-8s %s/%s\n", role, b.Provider, b.Model)
		}
	}
	if daemon.Running() != nil {
		fmt.Println("\n运行中的 opencode 会话下一个请求即生效，无需重启。")
	} else {
		fmt.Println("\n注意：代理没在跑，先 newgate start。")
	}
}

func cmdDoctor() {
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
	probs := config.Validate()
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
	st := config.LoadState()
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
		if takeover.IsTakenOver(t) {
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
		os.Exit(1)
	}
}

// checkProxyEnv 检查 HTTP(S)_PROXY 会不会把 127.0.0.1 的请求也劫走。
// 这是最阴的一类故障：客户端拿到 502 Bad Gateway（正向代理连不上 127.0.0.1），
// 但 newgate 日志里一条请求都没有——因为请求根本没到我们这。
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

// cmdAllLogs 一次性输出所有排查需要的东西，方便直接贴出来。
func cmdAllLogs() {
	line := func(t string) { fmt.Printf("\n===== %s =====\n", t) }

	line("版本与环境")
	fmt.Println(versionLine())
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
	if provs, err := config.LoadProviders(); err == nil {
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
	names, _ := config.ListProfiles()
	st := config.LoadState()
	for _, n := range names {
		mark := " "
		if n == st.ActiveProfile {
			mark = "*"
		}
		fb := ""
		if n == st.FallbackProfile {
			fb = "  (备用)"
		}
		fmt.Printf(" %s %s%s\n", mark, n, fb)
		if pr, err := config.LoadProfile(n); err == nil {
			for _, role := range config.Roles {
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
}

func sortedKeys(m map[string]config.Provider) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cmdLogs(n int, follow bool) {
	if follow {
		c := exec.Command("tail", "-n", strconv.Itoa(n), "-f", paths.LogFile())
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		_ = c.Run()
		return
	}
	b, err := ioutil.ReadFile(paths.LogFile())
	if err != nil {
		die(69, "读不到日志 "+paths.LogFile()+"："+err.Error())
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	fmt.Println(strings.Join(lines, "\n"))
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

// pingProxy 探活。必须绕过出站代理——否则 HTTP_PROXY 环境变量会让这个
// 请求被送去公司代理，代理连不上本机 loopback 回 502，我们误判"没起来"，
// 然后把刚起好的代理杀掉。
// waitProxy 等代理起来。wrapper 懒启动后要用。
func waitProxy(port int) {
	for k := 0; k < 60; k++ {
		if pingProxy(port) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func pingProxy(port int) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d/__newgate/status", port))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	// HTTP 走不通也可能是环境问题而非进程问题，用裸 TCP 再确认一次
	return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond)
}
