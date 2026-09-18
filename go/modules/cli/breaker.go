package cli

import (
	"fmt"
	"time"

	"github.com/rzbdz/newgate/go/lib/style"
	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
)

// cmdBreaker 只回答一件事：**现在哪些 binding 出问题了、为什么**。
//
// 为什么单独立一条命令，不并进 `metrics`：`metrics` 的「模型健康」段落
// （printModelHealth）故意把卡顿/不可用的详情藏起来只说个数量，理由写在
// 那段注释里——避免体检退化成日志墙。但排查「deepseek 怎么又被摘了」这
// 类问题时，恰恰要看那几条被藏起来的详情：什么时候摘的、还要等多久、
// 连续失败几次、记在哪本账上、上一次 probe 给的评级与延迟。
//
// 2026-09-17 半开重构后这条命令分两段：
//
//   - 被摘牌的：状态、账本、冷却到期时刻、原因、试探在不在飞；
//   - 只计数没摘牌的：请求形状错误（永不摘牌）和还没到阈值的失败。
//     第二段是这轮新加的可见面——以前「失败 1 次、闸还没开」在快照里
//     根本不存在，而形状错误只躺在 metrics 计数器里，用户看到「明明
//     能用却被摘了」查不到原因。
func cmdBreaker() int {
	info, ps := proxyState()
	if info == nil {
		return die(69, "代理没在运行（newgate start）——熔断表在 daemon 内存里")
	}
	if ps == nil {
		return die(69, fmt.Sprintf("连不上代理 127.0.0.1:%d（newgate doctor）", info.Port))
	}

	var open, counted []breakerapi.Status
	for _, b := range ps.Breakers {
		switch {
		case b.Open:
			open = append(open, b)
		case b.Fails > 0 || b.ShapeSkips > 0 || b.Spared > 0:
			counted = append(counted, b)
		}
	}

	if len(open) == 0 && len(counted) == 0 {
		fmt.Println(style.Title("newgate breaker", fmt.Sprintf("pid %d", info.PID)))
		fmt.Println(style.Hint("没有出问题的 binding"))
		return 0
	}

	fmt.Println(style.Title("newgate breaker",
		fmt.Sprintf("pid %d · %d 个被摘牌 · %d 个只计数", info.PID, len(open), len(counted))))

	if len(open) > 0 {
		t := style.NewTable("binding", "状态", "账本", "多久前摘的", "还要等", "连续失败", "原因")
		for _, b := range open {
			t.Row(b.Provider+"/"+b.Model, stateLabel(b), ruleLabel(b),
				agoLabel(b.OpenedAt), untilLabel(b.OpenUntil, b.Trial),
				fmt.Sprintf("%d", b.Fails), b.Reason)
		}
		fmt.Print(t.String())
		fmt.Println()
		fmt.Print(probeTable(open))
	}

	if len(counted) > 0 {
		fmt.Println()
		fmt.Println(style.Section("只计数、没摘牌") +
			style.Dim("   这些 binding 仍然在链上"))
		t := style.NewTable("binding", "账本", "连续失败", "请求形状", "救回", "上次 probe")
		for _, b := range counted {
			t.Row(b.Provider+"/"+b.Model, ruleLabel(b),
				fmt.Sprintf("%d", b.Fails), shapeLabel(b), sparedLabel(b), probeLabel(b))
		}
		fmt.Print(t.String())
	}

	fmt.Println()
	if spared := totalSpared(ps.Breakers); spared > 0 {
		// 「救回」是这轮新增的可见面：上闸前那次诊断探活把 binding 从摘牌边缘
		// 拉了回来。数字不回零（跨重启累计），所以它回答的是「这条链一共被
		// 误判过几次」，而不是「现在还有几次没清」。
		fmt.Println(style.Hint(fmt.Sprintf(
			"诊断探活累计救回 %d 次：真实流量数到阈值、但主动探活证明它还通，计数清零未摘牌", spared)))
	}
	// 半开之后解封不再只有 probe 一条路：真实流量在冷却期满后会自动被放行
	// 一次做试探，成了就合闸。probe 仍然是**立刻**改结论的手段。
	fmt.Println(style.Hint("冷却到期后会放行一次真实请求作试探：成功即合闸，失败则回闸并把冷却翻倍（上限 10 分钟）"))
	fmt.Println(style.Hint("  newgate probe        # 不等冷却，立刻用一次主动探活改结论"))
	fmt.Println(style.Hint("  newgate tier <档位>   # 看这个 binding 在链里排第几、是不是被跳过"))
	return 0
}

// probeTable 单独排一段：probe 结论是**另一个来源**（主动探活 vs 真实流量），
// 混在摘牌原因里会让人以为是同一件事。
func probeTable(rows []breakerapi.Status) string {
	t := style.NewTable("binding", "上次 probe", "延迟", "探活时间")
	for _, b := range rows {
		t.Row(b.Provider+"/"+b.Model, probeLabel(b), latencyLabel(b), checkedLabel(b))
	}
	return t.String()
}

func probeLabel(b breakerapi.Status) string {
	if b.Checked.IsZero() || b.Grade == "" {
		return style.Dim("未探活")
	}
	return string(b.Grade)
}

func latencyLabel(b breakerapi.Status) string {
	if b.Checked.IsZero() {
		return style.Dim("-")
	}
	return fmt.Sprintf("%dms", b.Latency)
}

func checkedLabel(b breakerapi.Status) string {
	if b.Checked.IsZero() {
		return style.Dim("-")
	}
	return fmt.Sprintf("%s 前", time.Since(b.Checked).Round(time.Second))
}

// stateLabel 把状态机的三态翻成中文。daemon 可能是旧的（优雅交接期间 CLI 与
// daemon 版本可以不同），老快照没有 state 字段，就从 Open 推——旧语义里
// 只有「摘了」和「没摘」两种。
func stateLabel(b breakerapi.Status) string {
	switch b.State {
	case "half-open":
		if b.Trial {
			return style.Yellow("半开·试探中")
		}
		return style.Yellow("半开·待试探")
	case "open":
		return style.Red("摘牌中")
	case "closed":
		return style.Green("正常")
	}
	if b.Open {
		return style.Red("摘牌中")
	}
	return style.Green("正常")
}

func ruleLabel(b breakerapi.Status) string {
	if b.Rule == "" {
		return style.Dim("-")
	}
	return b.Rule
}

func shapeLabel(b breakerapi.Status) string {
	if b.ShapeSkips == 0 {
		return style.Dim("-")
	}
	return fmt.Sprintf("%d 次", b.ShapeSkips)
}

// sparedLabel 显示「差点被摘、被诊断探活救回来」的累计次数。
func sparedLabel(b breakerapi.Status) string {
	if b.Spared == 0 {
		return style.Dim("-")
	}
	return style.Green(fmt.Sprintf("%d 次", b.Spared))
}

// totalSpared 汇总整张表的救回次数，供页脚那句话用。
func totalSpared(rows []breakerapi.Status) int {
	n := 0
	for _, b := range rows {
		n += b.Spared
	}
	return n
}

func agoLabel(at time.Time) string {
	if at.IsZero() {
		return style.Dim("-")
	}
	return fmt.Sprintf("%s 前", time.Since(at).Round(time.Second))
}

// untilLabel 说清「还要等多久」，以及在冷却是干什么用的：退避之后冷却会
// 越来越长（60s → 120s → … → 10 分钟），用户看到的数字对不上基准是正常的，
// 所以把试探状态也放进来。
func untilLabel(until time.Time, trial bool) string {
	if trial {
		return style.Yellow("试探在飞")
	}
	if until.IsZero() {
		return style.Dim("-")
	}
	if d := time.Until(until); d > 0 {
		return prettyDur(int(d.Seconds()))
	}
	return style.Green("已到期")
}
