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
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/gateway/dialect"
	"github.com/rzbdz/newgate/go/modules/gateway/health"
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

func healthDisplayRank(h health.Status) int {
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

func modelHealthState(h health.Status) string {
	if h.Open {
		score, _ := modelDisplayScore(h)
		if h.Grade == health.ProbeLaggy || score > 12000 {
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

func modelScoreLine(h health.Status) string {
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

func modelDisplayScore(h health.Status) (int, bool) {
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