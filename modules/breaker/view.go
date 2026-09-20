package breaker

// 本文件是健康表贡献给 web 界面的那一面：**一张只读的表**。
//
// 为什么是 breaker 报而不是界面自己去读 `/__newgate/status`：这张表的每一列
// 都是本模块写进去的字段（状态机的三态、记在哪本账上、冷却到期时刻、探活评级、
// 被救回的累计次数），而它们**怎么翻成人话**也是本模块的语义。界面自己去读
// 快照的话，它就得认识 `half-open` / `availability` 这些机器标记——那是判据，
// 不是文案（与 modules/gateway/view.go 那边同一条理由）。
//
// 这里与 CLI 那边同源：`newgate breaker` 的每一格文字都由下面这几个 `*Text`
// 函数产出（那边只是把结果套上颜色）。所以「跑到终端里看到的」与「网页上看到
// 的」不会各说各话——这正是拆分文字与着色两层的原因（见 stateText 的注释）。

import (
	"strings"
	"time"

	"github.com/rzbdz/newgate/lib/durarg"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
)

// healthConcepts 是健康表这一刻的样子。
//
// 它被调用的时机是**有人来看界面**，所以每一格都是「现在」的：冷却还剩多久、
// 探活是什么时候打的，都是按调用时刻算的（见 lib/view 的包注释）。
//
// 报**全部** binding，不只是出问题的那些：CLI 那条命令（`newgate breaker`）刻意
// 只报出事的，因为终端里一行行滚过去，没问题的行是噪音；而一张能停在屏幕上的
// 表不是——「哪些是好的」与「哪些坏了」是同一个问题的两半，而且好坏是相对的
// （一条能用的 binding 卡顿到什么程度，只有跟别的比才知道）。
func healthConcepts(table Breaker) ([]view.Concept, error) {
	rows := table.Snapshot()
	return []view.Concept{{
		ID:    "breaker.health",
		Kind:  view.KindTable,
		Title: i18n.T("Binding health", nil),
		Data:  healthTable(rows),
	}}, nil
}

// healthTable 把快照摆成一张表。列的取舍跟着 `newgate breaker` 走（同一批字段、
// 同一套说法），少了两列：
//
//   - **Reason**（摘牌原因）是上游/网关的原文，可能很长且带引号，塞进一格会把
//     整行撑变形。它属于「点开一条看详情」，不属于总览——等界面有了详情面板再放。
//   - **Checked**（探活时刻）与「Waiting」在同一张表里问的是两件不同的事，而
//     「多久以前」这种相对时间在网页上会过期（页面开着不动，数字就不对了）。
//     延迟与评级是绝对值，留下了。
func healthTable(rows []Status) view.Table {
	t := view.Table{Columns: []view.Column{
		{ID: "binding", Label: i18n.T("binding", nil)},
		{ID: "state", Label: i18n.T("State", nil)},
		{ID: "rule", Label: i18n.T("Ledger", nil)},
		{ID: "fails", Label: i18n.T("Failures", nil), Align: "right"},
		{ID: "until", Label: i18n.T("Waiting", nil)},
		{ID: "probe", Label: i18n.T("Last probe", nil)},
		{ID: "latency", Label: i18n.T("Latency", nil), Align: "right"},
	}}
	for _, b := range rows {
		state, stateTone := stateText(b)
		until, untilTone := untilText(b)
		t.Rows = append(t.Rows, map[string]view.Cell{
			"binding": {Text: b.Provider + "/" + b.Model},
			"state":   {Text: state, Tone: stateTone},
			"rule":    {Text: ruleText(b)},
			"fails":   failsCell(b),
			"until":   {Text: until, Tone: untilTone},
			"probe":   {Text: probeText(b)},
			"latency": latencyCell(b),
		})
	}
	return t
}

// failsCell 把「连续失败」与两个只计数不算失败的数摆在一格里。
//
// 挤在一格是**刻意**的：它们回答的是同一个问题（这条 binding 最近有多不健康），
// 而分成三列会让大部分格子是空的（形状错误与被救回都是少数 binding 才有的）。
//
// 每一段都是**完整短语**，用分隔符连起来——不是「基数 + 后缀」那种拼法。拼接
// 译文的碎片是 i18n 的老坑：英文里 "2 + 3 shape" 读得通，换个语序的语言就成了
// 乱码，而翻译的人看到的只是一截没有主语的片段，无从下手。
func failsCell(b Status) view.Cell {
	parts := []string{i18n.N("{n} failure", "{n} failures", b.Fails, nil)}
	if b.ShapeSkips > 0 {
		parts = append(parts, i18n.N("{n} shape error", "{n} shape errors", b.ShapeSkips, nil))
	}
	if b.Spared > 0 {
		parts = append(parts, i18n.N("{n} spared", "{n} spared", b.Spared, nil))
	}
	tone := ""
	if b.Fails > 0 || b.ShapeSkips > 0 {
		tone = view.ToneWarn
	}
	return view.Cell{Text: strings.Join(parts, " · "), Tone: tone}
}

func latencyCell(b Status) view.Cell {
	if b.Checked.IsZero() {
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: i18n.T("{ms}ms", i18n.A{"ms": b.Latency})}
}

// ---------- 文字与着色分家 ----------
//
// 下面这几个 `*Text` 是**唯一**说法来源，返回「给人看的字」+「它该是什么颜色」。
// CLI（command.go）与 web（上面那张表）各自消费它：终端把颜色套成 ANSI，网页
// 把它当语义交给 CSS。
//
// 为什么不让两边各写一份：这两处说的必须是同一件事，而它们最容易分家的地方
// 恰恰是**判据**（什么算「摘了」、账本名怎么翻）。分家之后症状是「终端说健康、
// 网页说半开」——同一个进程的同一份数据，两个界面各说各话，看的人只会怀疑数据
// 本身。颜色则必须分层：ANSI 转义在网页上是一串乱码，CSS 类名在终端里是一串
// 乱码，两边共用一个字符串是行不通的。

// stateText 是状态机三态的人话。返回的 tone 取 view.Tone*（空串 = 不着色）。
//
// daemon 可能是旧的（优雅交接期间 CLI 与 daemon 版本可以不同），老快照没有
// state 字段，就从 Open 推——旧语义里只有「摘了」和「没摘」两种。
//
// `b.State` 的取值（closed / open / half-open）是机器标记，只用来做判据；翻的
// 是给人看的那一侧。
func stateText(b Status) (string, string) {
	switch b.State {
	case "half-open":
		if b.Trial {
			return i18n.T("half-open · trial in flight", nil), view.ToneWarn
		}
		return i18n.T("half-open · awaiting trial", nil), view.ToneWarn
	case "open":
		return i18n.T("tripped", nil), view.ToneBad
	case "closed":
		return i18n.T("healthy", nil), view.ToneOK
	}
	if b.Open {
		return i18n.T("tripped", nil), view.ToneBad
	}
	return i18n.T("healthy", nil), view.ToneOK
}

// ruleText 把快照里的账本名（机器标记）翻回人话。认不出来的原样显示：优雅
// 交接期间旧 daemon 给的是**旧值**（当时的显示名），一个陌生值不该被吞掉。
func ruleText(b Status) string {
	if b.Rule == "" {
		return "-"
	}
	if name := bucketFromName(b.Rule).ruleName(); name != "" {
		return name
	}
	return b.Rule
}

// untilText 说清「还要等多久」，以及在冷却是干什么用的：退避之后冷却会越来越长
// （60s → 120s → … → 10 分钟），用户看到的数字对不上基准是正常的，所以把试探
// 状态也放进来。
//
// 「多久」是相对时间，网页上会过期（页面开着不动，数字就不对了）。这里仍然按
// 调用时刻算：这张表**每次请求现生成**（见 healthConcepts），所以数字与快照
// 同岁。真要停在一个不动的页面上，那需要的是前端自己走秒，不是后端给绝对时刻。
func untilText(b Status) (string, string) {
	if b.Trial {
		return i18n.T("trial in flight", nil), view.ToneWarn
	}
	if b.OpenUntil.IsZero() {
		return "-", ""
	}
	if d := time.Until(b.OpenUntil); d > 0 {
		return durarg.Format(int(d.Seconds())), ""
	}
	return i18n.T("expired", nil), view.ToneOK
}

// probeText 是上一次主动探活的评级。
//
// ProbeGrade 是 wire 契约里的档位名（fluent / usable / laggy / unavailable），
// 机器标记，原样显示——它同时也是 `newgate probe` 与 tier 链排序用的那套词。
func probeText(b Status) string {
	if b.Checked.IsZero() || b.Grade == "" {
		return i18n.T("never probed", nil)
	}
	return string(b.Grade)
}
