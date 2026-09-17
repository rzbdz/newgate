package cli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"

	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
	"github.com/rzbdz/newgate/go/modules/gateway/dialect"
	"github.com/rzbdz/newgate/go/modules/gateway/metrics"
	"github.com/rzbdz/newgate/go/modules/gateway/probe"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
	"github.com/rzbdz/newgate/go/modules/runtime/takeover"
)

func warnShellEnvConflict(agents agentapi.AgentCatalog, toolID string) {
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
	fmt.Println(style.Hint("shell 里已导出 " + strings.Join(conflict, ", ")))
	fmt.Println(style.Hint("接管时在子进程内覆盖，不影响 newgate；off 之后重新生效（即回到直连）"))
}

// cmdProbe 主动探活：对每个候选真打一发最小请求。
//
// 版式：进度走 stderr（stdout 留给结果表，`probe > f` 拿到的是干净的表），
// 结果一段三块——明细表 / 方言能力 / 汇总。以前进度和结果都往 stdout 里
// 混着写，重定向之后是一堆进度行夹着表格。
func cmdProbe(only string, asJSON bool) int {
	quiet := asJSON // JSON 模式不打进度，免得污染输出
	started := time.Now()
	st := store.LoadState()

	opts := probe.Options{
		Only:        only,
		Timeout:     st.Timeouts.ClassifierFirstByte(),
		SlowAfter:   st.Timeouts.ClassifierFirstByte(),
		Concurrency: 8,
		WaitTick:    5 * time.Second,
	}
	if !quiet {
		opts.OnPlan = func(ts []probe.Target) {
			fmt.Fprintf(os.Stderr, "探测 %d 个目标（并发 %d，同 (provider, model) 只打一次）\n",
				len(ts), opts.Concurrency)
			for _, t := range ts {
				fmt.Fprintf(os.Stderr, "  %s %s\n", style.Mark(style.Skip), t)
			}
			fmt.Fprintln(os.Stderr)
		}
		opts.OnDone = func(t probe.Target, status int, lat, totalLat time.Duration, err error) {
			mark := style.Mark(probeMark(err == nil && status == 200))
			st := "  -"
			if status > 0 {
				st = fmt.Sprintf("%3d", status)
			}
			msg := ""
			if err != nil {
				msg = "   " + firstLine(err.Error())
			}
			line := fmt.Sprintf("%s %s %s 主%dms 总%dms%s",
				mark, t.String(),
				st, lat.Milliseconds(), totalLat.Milliseconds(), msg)
			fmt.Fprintln(os.Stderr, style.WrapLine(line, "    "))
		}
		opts.OnWaiting = func(inflight map[probe.Target]time.Duration) {
			var parts []string
			for t, d := range inflight {
				parts = append(parts, fmt.Sprintf("%s %ds", t, int(d.Seconds())))
			}
			sort.Strings(parts)
			for _, part := range parts {
				fmt.Fprintln(os.Stderr, style.WrapLine(
					"        "+style.Mark(style.Skip)+" 等待中: "+part, "          "))
			}
		}
	}

	results, err := probe.Run(opts)
	if err != nil {
		return die(65, err.Error())
	}
	opened, healthErr := publishProbeHealth(results)
	if healthErr != nil && !quiet {
		fmt.Fprintln(os.Stderr, style.Item(style.Warn, "全局熔断表未更新："+healthErr.Error()))
	}
	if asJSON {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(b))
		if probe.FailedCount(results) > 0 {
			return 1
		}
		return 0
	}

	head := "全部 profile"
	if only != "" {
		head = "profile " + only
	}
	fmt.Println(style.Title("newgate probe", fmt.Sprintf("%s · %d 目标 · %s",
		head, probe.UniqueCount(results), time.Since(started).Round(time.Millisecond))))
	fmt.Println(style.Rule(72))

	// 明细：同一 profile 的后续行不再重复 profile 名（视觉分组，省一列宽度）。
	t := style.NewTable("profile", "档位", "上游/模型", "评级", "主延迟", "总耗时")
	last := ""
	var probeErrors []string
	for _, r := range results {
		p := r.Profile
		if p == last {
			p = ""
		} else {
			last = r.Profile
		}
		lat := style.Dim("-")
		if r.Latency > 0 {
			lat = fmt.Sprintf("%dms", r.Latency.Milliseconds())
		}
		if r.Err != "" {
			probeErrors = append(probeErrors, fmt.Sprintf("%s/%s：HTTP %d · %s",
				r.Provider, r.Model, r.Status, firstLine(r.Err)))
		}
		totalLat := style.Dim("-")
		if r.Total > 0 {
			totalLat = fmt.Sprintf("%dms", r.Total.Milliseconds())
		}
		t.Row(p, r.Role, r.Provider+"/"+r.Model,
			probeGrade(r, opts.SlowAfter), lat, totalLat)
	}
	fmt.Print(t.String())
	for _, detail := range probeErrors {
		fmt.Println(style.Item(style.Bad, detail))
	}
	fmt.Println(style.Hint("健康评分只用主探活延迟；总耗时还包含方言和 quirk 检查"))

	// 方言能力：probe 顺带探明的。count_tokens ✗ 的上游，Claude Code 的
	// 水位条走本地粗估（forward 层 lazy probe 也会自己学到这一点）。
	if entries := dialect.Snapshot(); len(entries) > 0 {
		fmt.Print(style.Section("方言能力") + style.Dim("   ✓ 支持  ✗ 探过不支持  ? 未探") + "\n")
		d := style.NewTable("上游/模型", "openai", "anthropic", "count_tokens")
		for _, e := range entries {
			d.Row(e.Provider+"/"+e.Model,
				dialectMark(e, dialect.CapOpenAI),
				dialectMark(e, dialect.CapAnthropic),
				dialectMark(e, dialect.CapCountTokens))
		}
		fmt.Print(d.String())
	}

	sums := probe.Summarize(results)
	fmt.Print(style.Section("汇总") + "\n")
	s := style.NewTable("profile", "通", "挂", "平均延迟", "结论")
	s.AlignRight(1)
	s.AlignRight(2)
	s.AlignRight(3)
	var healthy []string
	for _, sm := range sums {
		concl := style.Green("可用")
		switch {
		case sm.OK == 0:
			concl = style.Red("不可用")
		case sm.Bad > 0:
			concl = style.Yellow("部分可用")
		case sm.Grade == "fluent":
			concl = style.Green("流畅")
		}
		avg := style.Dim("-")
		if sm.OK > 0 {
			avg = fmt.Sprintf("%dms", sm.AvgMs)
			if sm.Bad == 0 {
				healthy = append(healthy, fmt.Sprintf("%s(%dms)", sm.Profile, sm.AvgMs))
			}
		}
		s.Row(sm.Profile, fmt.Sprintf("%d", sm.OK), fmt.Sprintf("%d", sm.Bad), avg, concl)
	}
	fmt.Print(s.String())

	fmt.Println(style.Hint("当前 profile：" + st.DefaultProfile))
	if healthErr == nil {
		fmt.Println(style.Hint(fmt.Sprintf(
			"全局熔断表已更新：本轮目标中仍有 %d 个 binding 熔断（慢阈值 %s）",
			opened, st.Timeouts.ClassifierFirstByte())))
		if opened > 0 {
			fmt.Println(style.Hint("至少隔离 60s；之后仅成功 probe 可以恢复"))
		}
	}

	// 当前 profile 有挂的就给出建议
	for _, sm := range sums {
		if sm.Profile == st.DefaultProfile && sm.Bad > 0 {
			fmt.Println()
			fmt.Println(style.Item(style.Warn, fmt.Sprintf("当前 profile %s 有 %d 个档位不可用", sm.Profile, sm.Bad)))
			if len(healthy) > 0 {
				fmt.Println(style.Bullet("全绿的：" + strings.Join(healthy, " · ")))
				fmt.Println(style.Bullet(style.Cyan("newgate --set-profile " + strings.SplitN(healthy[0], "(", 2)[0])))
			} else {
				fmt.Println(style.Bullet("没有全绿的 profile"))
			}
		}
	}
	if probe.FailedCount(results) > 0 {
		return 1
	}
	return 0
}

func publishProbeHealth(results []probe.Result) (int, error) {
	info, _ := proxyState()
	if info == nil || info.Port <= 0 {
		return 0, fmt.Errorf("daemon 未运行")
	}
	st := store.LoadState()
	type observation struct {
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Status    int    `json:"status"`
		LatencyMs int64  `json:"latency_ms"`
		Context   int    `json:"context_bytes"`
		Error     string `json:"error,omitempty"`
	}
	byTarget := map[string]observation{}
	for _, r := range results {
		if r.Provider == "" || r.Model == "" {
			continue
		}
		key := r.Provider + "/" + r.Model
		byTarget[key] = observation{
			Provider: r.Provider, Model: r.Model, Status: r.Status,
			LatencyMs: r.Latency.Milliseconds(), Context: 1, Error: r.Err,
		}
	}
	var keys []string
	for key := range byTarget {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var observations []observation
	for _, key := range keys {
		observations = append(observations, byTarget[key])
	}
	var response struct {
		Opened int `json:"opened"`
	}
	err := localPost(info.Port, "/__newgate/health", st.ControlToken,
		map[string]interface{}{"observations": observations}, &response)
	return response.Opened, err
}

// probeMark 探活结果的标记。跟 probe.Light() 的灯同义，但用本 CLI 统一的
// 符号集——表格里塞 emoji 会撑坏对齐，也不 geek。
func probeMark(ok bool) string {
	if ok {
		return style.OK
	}
	return style.Bad
}

func probeGrade(r probe.Result, slowAfter time.Duration) string {
	switch {
	case r.Status != http.StatusOK:
		return style.Red("不可用")
	case r.Latency > slowAfter:
		return style.Red("卡顿")
	case r.Latency >= 3*time.Second:
		return style.Yellow("可用")
	default:
		return style.Green("流畅")
	}
}

// cmdMetrics 展示全局 binding 评分与网关计数器。可用 binding 逐个列出，
// 卡顿/不可用只汇总数量；具体失败原因由 probe 输出，避免 metrics 退化成日志。
//
// 版式：按**分组**排（请求 / 链 / 超时 / 客户端 / 插件 / 兜底），组名只在
// 该组第一行出现。原始计数器名一列不少——用户会拿它去 grep 日志。
func cmdMetrics() int {
	info, ps := proxyState()
	if info == nil {
		return die(69, "代理没在运行（newgate start）——计数器在 daemon 内存里")
	}
	counter, uptime, ok := proxyMetrics(info.Port)
	if !ok {
		return die(69, fmt.Sprintf("连不上代理 127.0.0.1:%d（newgate doctor）", info.Port))
	}
	fmt.Println(style.Title("newgate metrics",
		fmt.Sprintf("pid %d · %s", info.PID, prettyDur(uptime))))
	if ps != nil {
		fmt.Println(style.Hint(fmt.Sprintf("%dreq/%derr · 计数随 daemon 重启归零", ps.Requests, ps.Failures)))
	} else {
		fmt.Println(style.Hint("计数随 daemon 重启归零"))
	}
	printModelHealth(ps)

	if len(counter) == 0 {
		fmt.Print(style.Section("请求计数") + "\n")
		fmt.Println(style.Dim("  无计数（daemon 启动后尚无请求）"))
	} else {
		t := style.NewTable("分组", "计数器", "次数", "说明")
		t.AlignRight(2)
		keys := metrics.SortedKeys(counter)
		// 按**组**排，组内再按名字。不加这一步的话字母序会让「链」和「超时」
		// 交错出现，分组那一列就白设了。
		sort.SliceStable(keys, func(i, j int) bool {
			gi, gj := metricRank(keys[i]), metricRank(keys[j])
			if gi != gj {
				return gi < gj
			}
			return keys[i] < keys[j]
		})
		lastGroup := ""
		for _, k := range keys {
			g := metricGroup(k)
			label := style.Dim(g)
			if g == lastGroup {
				label = ""
			}
			lastGroup = g
			t.Row(label, k, fmt.Sprintf("%d", counter[k]), style.Dim(metricHint(k)))
		}
		fmt.Print(style.Section("请求计数") + "\n")
		fmt.Print(t.String())
	}
	return 0
}

func printModelHealth(ps *proxyInfo) {
	if ps == nil {
		return
	}
	snap, err := store.Load()
	if err != nil {
		return
	}
	statuses := healthFromProxy(ps)
	bindings := map[string]bool{}
	for _, profile := range snap.Profiles {
		for _, candidates := range profile.Roles {
			for _, binding := range candidates {
				if !binding.IsRef() && binding.Provider != "" && binding.Model != "" {
					if provider, ok := snap.Providers.Providers[binding.Provider]; ok && provider.Key() != "" {
						bindings[binding.String()] = true
					}
				}
			}
		}
		if profile.Fallback != nil && !profile.Fallback.IsRef() {
			if provider, ok := snap.Providers.Providers[profile.Fallback.Provider]; ok && provider.Key() != "" {
				bindings[profile.Fallback.String()] = true
			}
		}
	}
	for _, binding := range special.Bindings(snap.State) {
		if provider, ok := snap.Providers.Providers[binding.Provider]; ok && provider.Key() != "" {
			bindings[binding.String()] = true
		}
	}
	var names []string
	for binding := range bindings {
		names = append(names, binding)
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, z := statuses[names[i]], statuses[names[j]]
		if a.Open != z.Open {
			return !a.Open
		}
		ra, rz := healthDisplayRank(a), healthDisplayRank(z)
		if ra != rz {
			return ra < rz
		}
		return names[i] < names[j]
	})

	counts := map[string]int{}
	for _, name := range names {
		h := statuses[name]
		state := modelHealthState(h)
		counts[state]++
	}
	blocked := counts["卡顿"] + counts["不可用"]
	fmt.Print(style.Section("模型健康") + "\n")
	fmt.Println(style.Hint(fmt.Sprintf(
		"可用 %d（流畅 %d · 可用 %d · 未评分 %d）· 卡顿 %d · 不可用 %d",
		len(names)-blocked, counts["流畅"], counts["可用"], counts["未评分"],
		counts["卡顿"], counts["不可用"])))
	for _, name := range names {
		h := statuses[name]
		state := modelHealthState(h)
		if state == "卡顿" || state == "不可用" {
			continue
		}
		fmt.Println(style.Item(style.Skip, name))
		fmt.Println(style.Hint("    " + modelScoreLine(h)))
	}
	if blocked > 0 {
		fmt.Println(style.Hint("卡顿/不可用详情：newgate probe"))
	}
}

func healthDisplayRank(h breakerapi.Status) int {
	switch modelHealthState(h) {
	case "流畅":
		return 0
	case "未评分":
		return 1
	case "可用":
		return 2
	default:
		return 3
	}
}

func modelHealthState(h breakerapi.Status) string {
	if h.Open {
		score, _ := modelDisplayScore(h)
		if h.Grade == breakerapi.ProbeLaggy || score > 12000 {
			return "卡顿"
		}
		return "不可用"
	}
	score, sampled := modelDisplayScore(h)
	switch {
	case sampled && score < 3000:
		return "流畅"
	case sampled && score <= 12000:
		return "可用"
	case sampled:
		return "卡顿"
	default:
		return "未评分"
	}
}

func modelScoreLine(h breakerapi.Status) string {
	const labels = "≤4K,≤32K,≤128K,>128K"
	names := strings.Split(labels, ",")
	var scores []string
	for i, n := range h.Buckets {
		if n > 0 {
			latency := fmt.Sprintf("%dms", h.Scores[i])
			if h.Scores[i] == 0 {
				latency = "<1ms"
			}
			scores = append(scores, fmt.Sprintf("%s %s/%d次", names[i], latency, n))
		}
	}
	if len(scores) == 0 && h.ScoreMs > 0 {
		samples := h.Samples
		if samples < 1 {
			samples = 1
		}
		scores = append(scores, fmt.Sprintf("≤4K %dms/%d次", h.ScoreMs, samples))
	}
	if len(scores) == 0 {
		return "未评分"
	}
	return modelHealthState(h) + " · " + strings.Join(scores, " · ")
}

func modelDisplayScore(h breakerapi.Status) (int, bool) {
	for i, n := range h.Buckets {
		if n > 0 {
			return h.Scores[i], true
		}
	}
	return h.ScoreMs, h.ScoreMs > 0
}

// metricOrder 组的显示顺序：先「请求」，再按一次请求会依次遇到的
// 链 → 超时 → 兜底 → 插件，最后是熔断与客户端。这个顺序本身在讲请求的
// 生命周期，比字母序有用。
var metricOrder = []string{"请求", "链", "超时", "兜底", "插件", "熔断", "客户端", "其他"}

func metricRank(k string) int {
	g := metricGroup(k)
	for i, name := range metricOrder {
		if name == g {
			return i
		}
	}
	return len(metricOrder)
}

// metricGroup 计数器归属的组。分组是给人看的锚点——一眼扫过就知道
// 「有没有在换人」「有没有超时」，不用逐个读计数器名。
func metricGroup(k string) string {
	switch {
	case strings.HasPrefix(k, "requests."):
		return "请求"
	case strings.HasPrefix(k, "chain."):
		return "链"
	case strings.HasPrefix(k, "timeout."):
		return "超时"
	case strings.HasPrefix(k, "client."):
		return "客户端"
	case strings.HasPrefix(k, "breaker."):
		return "熔断"
	case strings.HasPrefix(k, "special."):
		return "插件"
	case strings.HasPrefix(k, "count_tokens."):
		return "兜底"
	}
	return "其他"
}

// metricHint 计数器名字的人话注释。没列出的不硬凑——空说明比编一句好。
func metricHint(k string) string {
	switch {
	case k == "requests.total":
		return "进入网关的请求"
	case k == "count_tokens.forwarded":
		return "转发上游取真值"
	case k == "count_tokens.local":
		return "本地粗估兜底（上游无此端点）"
	case k == "count_tokens.probe_404":
		return "lazy probe 404，记为「上游不支持」"
	case k == "timeout.first_byte.non_stream":
		return "首字节超时（非流式），沿链下移"
	case k == "timeout.first_byte.stream":
		return "首字节超时（流式），沿链下移"
	case strings.HasPrefix(k, "timeout.first_byte"):
		return "首字节超时，沿链下移"
	case k == "chain.failover":
		return "前序候选失败，换到后续候选后成功"
	case k == "chain.step_failed":
		return "链上某站失败（连接 / 可转移错误）"
	case k == "chain.budget_exhausted":
		return "链总预算用尽"
	case k == "client.cancel":
		return "客户端主动取消"
	case k == "breaker.opened":
		return "熔断器打开，provider 暂时摘除"
	case k == "breaker.skipped.shape_error":
		return "请求形状错误（如 deepseek reasoning-400），跳过熔断记账"
	case strings.HasPrefix(k, "special."):
		if hint, ok := special.MetricHint(k); ok {
			return hint
		}
		return "插件改写了请求（逐条有日志）"
	}
	return ""
}

// prettyMs 毫秒 → 人话。链预算是按 ms 配的（state.json 里 120000），
// 打印时不该原样甩 120000ms 给用户。
func prettyMs(ms int) string {
	return (time.Duration(ms) * time.Millisecond).String()
}

func prettyDur(sec int) string {
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return d.String()
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// check 一次体检里的一项。
//
// 设计是「绿的只占一行，红的才展开」：体检命令每天跑，全绿时应该一眼扫完；
// 出问题时才需要路径、原因、修法。以前每行都顶着绝对路径和 ✓，等于把
// 有用的信息埋进噪声里 —— 用户会开始跳着看，然后就漏掉真正的那条。
type check struct {
	label   string
	mark    string
	line    string   // 一句话结论
	details []string // 只在有问题时展开
}

func (c check) print() {
	fmt.Println(style.Field(c.label, style.Mark(c.mark)+" "+c.line))
	for _, d := range c.details {
		fmt.Println(style.Bullet(style.Dim(d)))
	}
}

func cmdDoctor(service *service) int {
	fmt.Println(style.Title("newgate doctor", Version))
	fmt.Println(style.Rule(64))
	checks := []check{
		checkConfig(),
		checkChain(),
		checkEnv(),
		checkProxy(),
		checkTakeover(service.agents),
		checkBackups(),
	}
	for _, item := range service.moduleDiagnostics() {
		mark := style.Skip
		switch item.State {
		case "ok":
			mark = style.OK
		case "warn":
			mark = style.Warn
		case "bad":
			mark = style.Bad
		}
		checks = append(checks, check{
			label: item.Label, mark: mark, line: item.Line, details: item.Details,
		})
	}

	fmt.Println()
	bad := 0
	for _, c := range checks {
		if c.mark == style.Bad {
			bad++
		}
		c.print()
	}

	fmt.Println()
	if bad == 0 {
		fmt.Println(style.Green("全部通过"))
		return 0
	}
	fmt.Printf("%s %d 项异常\n", style.Mark(style.Bad), bad)
	return 1
}

// checkConfig 三个必需文件在不在。
func checkConfig() check {
	c := check{label: "文件"}
	var missing, ok []string
	for _, p := range []string{paths.ProvidersFile(), paths.StateFile(), paths.Mappings()} {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, filepath.Base(p))
		} else {
			ok = append(ok, filepath.Base(p))
		}
	}
	extra := ""
	if names, err := store.ListProfiles(); err == nil {
		extra = fmt.Sprintf(" · %d 个 profile", len(names))
	}
	if len(missing) > 0 {
		c.mark = style.Bad
		c.line = strings.Join(missing, " · ") + " 不存在"
		c.details = append(c.details, "newgate init 铺开默认配置")
		return c
	}
	c.mark = style.OK
	c.line = strings.Join(ok, " · ") + extra
	return c
}

// checkChain profile 引用的 provider 与 key 齐不齐。
func checkChain() check {
	c := check{label: "链路"}
	probs := store.Validate()
	names, _ := store.ListProfiles()
	if len(probs) == 0 {
		c.mark = style.OK
		c.line = fmt.Sprintf("%d 个 profile 的 provider 与 key 均可用", len(names))
		return c
	}
	c.mark = style.Bad
	c.line = fmt.Sprintf("%d 处配置问题", len(probs))
	c.details = probs
	return c
}

// checkEnv 出站代理会不会把 loopback 请求劫走。
//
// 这是最隐蔽的一类故障：newgate 日志里一条请求都没有，因为请求根本没到我们
// 这——客户端发给了 http_proxy，代理连不上 127.0.0.1 就回 502。
func checkEnv() check {
	c := check{label: "环境"}
	set := proxyEnvSet()
	if len(set) == 0 {
		c.mark = style.OK
		c.line = "无出站代理变量"
		return c
	}
	noProxy := os.Getenv("no_proxy") + "," + os.Getenv("NO_PROXY")
	covered := 0
	for _, want := range []string{"127.0.0.1", "localhost"} {
		if strings.Contains(noProxy, want) {
			covered++
		}
	}
	if covered == 2 {
		c.mark = style.OK
		c.line = "有出站代理；NO_PROXY 已放行 loopback"
		c.details = append(c.details, set...)
		return c
	}
	c.mark = style.Bad
	c.line = "有出站代理；NO_PROXY 未放行 127.0.0.1 / localhost"
	c.details = append(c.details, set...)
	c.details = append(c.details,
		"后果：客户端把 127.0.0.1:8899 的请求交给出站代理，连不上即 502；",
		"      newgate 日志不会留下任何记录。",
		"修复（写进 shell rc 后重开客户端）：",
		`  export NO_PROXY="127.0.0.1,localhost,::1,$NO_PROXY"`,
		`  export no_proxy="$NO_PROXY"`)
	return c
}

func proxyEnvSet() []string {
	var out []string
	for _, n := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"all_proxy", "ALL_PROXY"} {
		if v := os.Getenv(n); v != "" {
			out = append(out, n+"="+v)
		}
	}
	return out
}

// checkProxy 代理进程与端口的两种失败要分开说：进程死了可以重启，
// 端口在听却探不通是环境问题，重启一百次也没用。
func checkProxy() check {
	c := check{label: "代理"}
	st := store.LoadState()
	info, ps := proxyState()
	switch {
	case info == nil:
		c.mark = style.Skip
		c.line = fmt.Sprintf("未运行（端口 %d）", st.Port)
		c.details = append(c.details, "newgate start")
	case ps != nil:
		c.mark = style.OK
		c.line = fmt.Sprintf("pid %d · 127.0.0.1:%d · %s", info.PID, info.Port, prettyDur(ps.UptimeS))
	default:
		c.mark = style.Bad
		c.line = fmt.Sprintf("pid %d 存活，127.0.0.1:%d HTTP 探活失败", info.PID, info.Port)
		c.details = append(c.details,
			"进程正常，请求到不了它；常见于出站代理劫持 loopback（见「环境」一项）。")
	}
	return c
}

// checkTakeover 被改写的目标文件。
func checkTakeover(agents agentapi.AgentCatalog) check {
	c := check{label: "接管"}
	var on, off []string
	for _, id := range agents.Names() {
		agent, ok := agents.Get(id)
		if !ok || agent.Config == nil {
			continue
		}
		for _, target := range agent.Config.Targets() {
			if _, err := os.Stat(target); err != nil {
				continue
			}
			if agent.Config.IsTakenOver(target) {
				on = append(on, filepath.Base(target))
			} else {
				off = append(off, filepath.Base(target))
			}
		}
	}
	if len(on) == 0 {
		c.mark = style.Skip
		c.line = "无文件被改写"
		return c
	}
	c.mark = style.OK
	c.line = strings.Join(on, " · ")
	if len(off) > 0 {
		c.details = append(c.details, "未接管："+strings.Join(off, " · "))
	}
	return c
}

// checkBackups 逃生舱有没有准备好：original/ 里有东西，`newgate stop` 才还原得回去。
func checkBackups() check {
	c := check{label: "备份"}
	orig := filepath.Join(paths.BackupDir(), "original")
	ents, err := ioutil.ReadDir(orig)
	if err != nil || len(ents) == 0 {
		c.mark = style.Skip
		c.line = "无原始备份"
		c.details = append(c.details, "接管过配置文件之后才会生成")
		return c
	}
	c.mark = style.OK
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	c.line = fmt.Sprintf("%d 份原配置 · newgate stop 可还原", len(ents))
	c.details = append(c.details, "backups/original/ "+strings.Join(names, " · "))
	return c
}

// logTailDefault 非终端输出（管道 / 重定向）时的行数。终端上给全量，
// 由分页器兜着——和 journalctl 一样，重定向时才需要收敛。
const logTailDefault = 40

// cmdLogs 日志。行为对齐 journalctl：
//
//	newgate logs          终端上给全量日志，经分页器（内容不满一屏直接给）
//	newgate logs 200      只看最后 200 行
//	newgate logs -n 200   同上
//	newgate logs -f       跟随（tail -F）
//
// 分页只在 stdout 是终端时发生：`newgate logs > f` 或管道里必须老实打印，
// 否则脚本会挂在等用户按键上。
func cmdLogs(n int, follow bool) int {
	auto := n <= 0
	if follow {
		// -F 而不是 -f：logx 按 16MB 轮转，换文件后 -f 会跟丢，
		// 表现为「日志突然不动了」。
		k := n
		if k <= 0 {
			k = logTailDefault
		}
		c := exec.Command("tail", "-n", strconv.Itoa(k), "-F", paths.LogFile())
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		_ = c.Run()
		return 0
	}
	b, err := ioutil.ReadFile(paths.LogFile())
	if err != nil {
		return die(69, "读不到日志 "+paths.LogFile()+"："+err.Error())
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		fmt.Println(style.Dim("  日志是空的"))
		return 0
	}
	limit := n
	if auto {
		limit = logTailDefault
		if style.TTY() {
			limit = 0 // 终端：全量交给分页器
		}
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return pageOut(strings.Join(lines, "\n") + "\n")
}

// pageOut 经分页器输出。PAGER 优先；否则 less（-FRX：不满一屏自动退、
// 保留颜色、不动 termcap），没有 less 就退回 more。
//
// 分页器只是**显示**手段，任何一步失败都退回直接打印——不能因为没装
// 分页器就让用户看不到日志。
func pageOut(s string) int {
	if !style.TTY() {
		fmt.Print(s)
		return 0
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		switch {
		case haveCmd("less"):
			pager = "less -FRX"
		case haveCmd("more"):
			pager = "more"
		default:
			fmt.Print(s)
			return 0
		}
	}
	c := exec.Command("/bin/sh", "-c", pager)
	c.Stdin = strings.NewReader(s)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		fmt.Print(s)
	}
	return 0
}

func haveCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// logCount 解析行数：`newgate logs 200` 或 `newgate logs -n 200`。
// 都没给返回 0 = 自动（终端全量，管道取尾巴）。
func logCount(args []string) int {
	if v := findFlag(args, "-n", "--lines"); v != "" {
		var k int
		if _, err := fmt.Sscanf(v, "%d", &k); err == nil && k > 0 {
			return k
		}
	}
	return intArg(args, 1, 0)
}

func cmdAllLogs(agents agentapi.AgentCatalog) int {
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
	cmdStatus(agents)

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
			// 两种方言分家的上游：另一个 base 也报出来，否则「claude 的流量
			// 到底发去哪」在 doctor 里是黑盒。
			if p.AnthropicURL != "" {
				fmt.Printf("  %-14s %-45s （anthropic 方言走这条）\n", "", p.AnthropicURL)
			}
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
	var configTargets []string
	for _, id := range agents.Names() {
		agent, ok := agents.Get(id)
		if !ok || agent.Config == nil {
			continue
		}
		configTargets = append(configTargets, agent.Config.Targets()...)
	}
	sort.Strings(configTargets)
	for _, t := range configTargets {
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

// dialectMark 一格能力灯：✓ 支持 / ✗ 探过明确不支持 / ? 没探到。
func dialectMark(e dialect.Entry, c dialect.Cap) string {
	if e.Known&c == 0 {
		return "?"
	}
	if e.Supports&c != 0 {
		return "✓"
	}
	return "✗"
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

func cmdStatus(agents agentapi.AgentCatalog) int {
	st := store.LoadState()
	info, ps := proxyState()

	fmt.Println(style.Title("newgate "+Version, buildTimeDisplay()))
	fmt.Println(style.Rule(64))

	// 代理（数据面）：它挂了，所有走 newgate 的工具一起挂，所以排第一行。
	switch {
	case info == nil:
		fmt.Println(style.Field("代理", style.Dim("未运行")+"    newgate start"))
	case ps == nil:
		fmt.Println(style.Field("代理", style.Yellow("端口无响应")+
			fmt.Sprintf("   pid %d · 127.0.0.1:%d", info.PID, info.Port)))
		fmt.Println(style.Hint("进程在，端口不通；常见于出站代理劫持 loopback。newgate doctor"))
	default:
		fmt.Println(style.Field("代理", style.Green("● 运行中")+fmt.Sprintf(
			"   pid %d · 127.0.0.1:%d · %s · %dreq/%derr",
			info.PID, info.Port, prettyDur(ps.UptimeS), ps.Requests, ps.Failures)))
	}

	// 接管：回答「谁的命令现在会走 newgate」。期望态（on/off 过什么）和现实态
	// （磁盘上真装了什么）不一致，正是那两个对称故障的现场。
	fmt.Println(style.Field("接管", takeoverStatusLine(ps)))

	// 配置：用哪个 profile。
	fmt.Println(style.Field("配置", configLine(agents, st)))
	for _, item := range special.Statuses(st) {
		fmt.Println(style.Field(item.Label, item.Value))
	}

	if flags := statusFlags(st); flags != "" {
		fmt.Println(style.Field("开关", flags))
	}

	pr, err := store.LoadProfile(st.DefaultProfile)
	if err != nil {
		fmt.Println(style.Item(style.Warn, fmt.Sprintf("默认 profile %q 读不出: %v", st.DefaultProfile, err)))
		return 0
	}
	provs, _ := store.LoadProviders()

	fmt.Print(style.Section("档位绑定") + style.Dim("   profile "+st.DefaultProfile) + "\n")
	t := style.NewTable("档位", "绑定", "备注")
	for _, role := range domain.Roles {
		b, ok := pr.Resolve(role)
		if !ok {
			t.Row(style.Dim(role), style.Dim("未绑定"), "")
			continue
		}
		note := ""
		if provs != nil {
			if p, exists := provs.Providers[b.Provider]; !exists {
				note = style.Red("provider 未定义")
			} else if p.Key() == "" {
				note = style.Yellow("缺 api_key")
			}
		}
		t.Row(style.Cyan(role), b.String(), note)
	}
	fmt.Print(t.String())

	if snap, err := store.Load(); err == nil {
		steps, skips := resolve.BuildChain("normal", snap.Profiles, snap.Providers, resolve.Opts{
			Active: st.DefaultProfile, Available: availableFromProxy(ps),
			Rank:     rankFromProxy(ps),
			MaxSteps: st.Chain.Attempts()})
		fmt.Print(style.Section("fallback 链") + style.Dim("   normal 档，按序尝试") + "\n")
		if len(steps) == 0 {
			fmt.Println(style.Item(style.Warn, "无可用候选   newgate tier normal"))
		} else {
			fmt.Print(bindingChain(steps, "  "))
			if tail := chainTail(steps, skips); tail != "" {
				fmt.Println(style.Hint(tail))
			}
		}
	}
	return 0
}

// takeoverStatusLine 一行说清谁在走 newgate，有异常才展开。
func takeoverStatusLine(ps *proxyInfo) string {
	var on, off []string
	wanted := 0
	for _, s := range takeover.List() {
		if s.Wanted {
			wanted++
		}
		switch {
		case s.Active:
			on = append(on, s.Agent+style.Dim(" ")+style.Mark(style.OK))
		case s.Wanted:
			on = append(on, s.Agent+style.Dim(" ")+style.Mark(style.Bad))
		default:
			off = append(off, s.Agent)
		}
	}
	if len(on) == 0 {
		line := style.Dim("全部直连")
		if len(off) > 0 {
			line += "   " + style.Dim("newgate start / on <agent>")
		}
		return line
	}
	line := "   " + strings.Join(on, "   ")
	if len(off) > 0 {
		line += "   " + style.Dim(strings.Join(off, " ")+" off")
	}
	// 代理在跑却有 agent 想接管没接管上：start/build 之后漏了一步，
	// 不补的话那个工具会静默直连。
	if ps != nil && countActive() < wanted {
		line += "\n" + style.Hint(style.Yellow("有 agent 声明接管但未生效，重跑 newgate start"))
	}
	return line
}

func countActive() int {
	n := 0
	for _, s := range takeover.List() {
		if s.Active {
			n++
		}
	}
	return n
}

// configLine 一行说清用哪个 profile，有 per-agent 覆盖才展开。
func configLine(agents agentapi.AgentCatalog, st *domain.State) string {
	line := style.Cyan(st.DefaultProfile) + style.Dim(" 默认")
	over := 0
	var parts []string
	for _, id := range sortedAgentIDs(agents) {
		if p := st.Active[id]; p != "" && p != st.DefaultProfile {
			parts = append(parts, id+" → "+style.Cyan(p))
			over++
		}
	}
	if over > 0 {
		line += "   " + strings.Join(parts, "   ")
	}
	return line
}

// chainTail 链尾的一句话总结：还有多少候选被跳过、去哪儿看原因。
func chainTail(steps []resolve.Step, skips []resolve.Skip) string {
	if len(skips) == 0 {
		return fmt.Sprintf("链上 %d 站", len(steps))
	}
	reasons := map[string]int{}
	for _, s := range skips {
		reasons[skipKind(s.Reason)]++
	}
	var parts []string
	for _, k := range skipKinds {
		if n := reasons[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	return fmt.Sprintf("链上 %d 站；跳过 %d（%s）   newgate tier normal",
		len(steps), len(skips), strings.Join(parts, " · "))
}

// skipKind 把 skip 的自由文本归成几个可数的类目。
func skipKind(reason string) string {
	switch {
	case strings.Contains(reason, "excluded"):
		return "excluded"
	case strings.Contains(reason, "熔断"):
		return "熔断"
	case strings.Contains(reason, "maxAttempts"):
		return "超出 maxAttempts"
	case strings.Contains(reason, "重复") || strings.Contains(reason, "去重"):
		return "去重"
	case strings.Contains(reason, "未定义"):
		return "未定义"
	case strings.Contains(reason, "api_key"):
		return "没 key"
	case strings.Contains(reason, "已禁用"):
		return "已禁用"
	case strings.Contains(reason, "成环"):
		return "引用成环"
	}
	return "其他"
}

// statusFlags 一行列出非默认开关。默认状态不占版面。
func statusFlags(st *domain.State) string {
	var f []string
	switch {
	case st.DebugActive():
		s := style.Yellow("debug=on")
		if st.DebugUntil != "" {
			s += style.Dim("（到 " + st.DebugUntil + "）")
		}
		f = append(f, s)
	case st.Debug:
		f = append(f, style.Yellow("debug=已过期"))
	}
	if !st.RepairEnabled() {
		f = append(f, style.Yellow("schema-repair=off"))
	}
	switch {
	case !st.SpecialEnabled():
		f = append(f, style.Yellow("special_treatment=off"))
	case len(st.SpecialOff) > 0:
		f = append(f, style.Yellow("special 关了 "+strings.Join(st.SpecialOff, ",")))
	}
	return strings.Join(f, "   ")
}

// sortedAgentIDs 稳定顺序的已知 agent 列表。
func sortedAgentIDs(agents agentapi.AgentCatalog) []string {
	ids := agents.Names()
	sort.Strings(ids)
	return ids
}

// routingOf 已被 runtime/takeover.List() 取代——那里是接管状态的唯一事实源，
// CLI / TUI / Web 都读同一份，不再各自判断一遍机制。
