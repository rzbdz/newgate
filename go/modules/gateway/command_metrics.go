package gateway

// 本文件是 `newgate metrics`：网关计数器 + 模型健康概览。
//
// **为什么它住在这里而不是 modules/cli**（2026-09-18）：计数器是网关自己的
// 语义——哪些键、怎么分组、什么含义，全在 gateway/metrics 里定义；界面只是
// 把它排版出来。命令留在界面时，「metricOrder」这张分组顺序表和「模型健康」
// 那段渲染也一并留在界面，于是界面里躺着一堆和数据面有关的知识（用户的原话：
// 「cli 目录里面不能包含任何和 cli 展示无关的东西，什么 metrics 什么 probe」）。
//
// 搬过来之后界面不再 import gateway/metrics —— 它连这些计数器的名字都不认识。
//
// 依赖方向：gateway → cli/extension（叶子契约），不是 → modules/cli。所以这是
// 一次普通的「owner 往界面注入自己的命令」，没有回边。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/go/lib/durarg"
	"github.com/rzbdz/newgate/go/lib/style"
	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	"github.com/rzbdz/newgate/go/modules/breaker/status"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/go/modules/gateway/metrics"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// Rank 只在**节内**排序；节的先后由界面的槽位表决定（见 cli/extension.Section）。
// 数字与 modules/cli 里那一套同源，留空档给插队。
const (
	rankObserve     = 40
	rankMaintenance = 50
)

type metricsCommand struct{}

var (
	_ cliapi.Command    = (*metricsCommand)(nil)
	_ cliapi.Documented = (*metricsCommand)(nil)
)

func (metricsCommand) Names() []string { return []string{"metrics", "stat"} }

func (metricsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage: "metrics", Summary: "代理计数器：拦截 / 超时 / 转移 / 改道"}
}

func (metricsCommand) Run(host cliapi.Host, _ []string) int { return runMetrics(host) }

// cmdMetrics 展示全局 binding 评分与网关计数器。可用 binding 逐个列出，
// 卡顿/不可用只汇总数量；具体失败原因由 probe 输出，避免 metrics 退化成日志。
//
// 版式：按**分组**排（请求 / 链 / 超时 / 客户端 / 插件 / 兜底），组名只在
// 该组第一行出现。原始计数器名一列不少——用户会拿它去 grep 日志。
func runMetrics(host cliapi.Host) int {
	info, ps := controlplane.State()
	if info == nil {
		return host.Die(69, "代理没在运行（newgate start）——计数器在 daemon 内存里")
	}
	counter, uptime, ok := controlplane.Metrics(info.Port)
	if !ok {
		return host.Die(69, fmt.Sprintf("连不上代理 127.0.0.1:%d（newgate doctor）", info.Port))
	}
	fmt.Println(style.Title("newgate metrics",
		fmt.Sprintf("pid %d · %s", info.PID, durarg.Format(uptime))))
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
			g := metrics.Group(k)
			label := style.Dim(g)
			if g == lastGroup {
				label = ""
			}
			lastGroup = g
			t.Row(label, k, fmt.Sprintf("%d", counter[k]), style.Dim(metrics.Hint(k)))
		}
		fmt.Print(style.Section("请求计数") + "\n")
		fmt.Print(t.String())
	}
	return 0
}

// metricOrder 组的显示顺序：先「请求」，再按一次请求会依次遇到的
// 链 → 超时 → 兜底 → 插件，最后是熔断与客户端。这个顺序本身在讲请求的
// 生命周期，比字母序有用。
var metricOrder = []string{"请求", "链", "超时", "兜底", "插件", "熔断", "客户端", "其他"}

func printModelHealth(ps *controlplane.Doc) {
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

// modelHealthState 把一行健康快照翻成给人看的档位名。
//
// 注意它读的是**分档明细**（Scores/Buckets），不是 daemon 算好的排序键 Rank
// ——这里要显示「哪个上下文档位多少毫秒」，而 Rank 只编码 ≤4K 那一档。
//
// 档位名与阈值来自 breaker/status（**只有一份**，2026-09-18）：这里曾经自己
// 抄了一份 3000/12000，与 breaker 的排序阈值各自演化。
func modelHealthState(h breakerapi.Status) string {
	if h.Open {
		score, sampled := modelDisplayScore(h)
		if h.Grade == breakerapi.ProbeLaggy || status.Grade(score, sampled) == status.LatencySlow {
			return "卡顿"
		}
		return "不可用"
	}
	score, sampled := modelDisplayScore(h)
	return status.Grade(score, sampled).String()
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

// metricRank 表格里的分组顺序。**顺序**是排版决定，留在 CLI；**归属**是数据面
// 知识，归 modules/gateway/metrics（见那边的 Group）。
func metricRank(k string) int {
	g := metrics.Group(k)
	for i, name := range metricOrder {
		if name == g {
			return i
		}
	}
	return len(metricOrder)
}

// healthFromProxy 把 daemon 的熔断表按 "provider/model" 索引成一次命令内的快照。
func healthFromProxy(ps *controlplane.Doc) map[string]breakerapi.Status {
	out := map[string]breakerapi.Status{}
	if ps != nil {
		for _, s := range ps.Breakers {
			out[s.Provider+"/"+s.Model] = s
		}
	}
	return out
}
