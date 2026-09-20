package runtime

// 本文件是接管状态贡献给 web 界面的那一面：**哪些客户端在走 newgate**。
//
// 为什么是 runtime 报：接管是本模块写出来的东西——`on/off` 改哪个文件、PATH 里
// 放了什么、state.json 里记了什么意愿，全是本模块的词汇。界面自己去读那些文件的话，
// 它就得认识 shim 与 config 两种机制的区别（见 takeover 包），而那正是「谁写的东西
// 谁自己说」这条规矩要避免的（与 diagnostics.go 把两条体检留在这里同一个理由）。
//
// # 三态判据只留一份
//
// 「接管没有」不是一个布尔值，是**三种**状态，而它们各自对应一个真实的现场
// （见 takeoverLine 的注释）：
//
//	现实态装了  = 接管生效了
//	意愿有、现实没有 = 声明了却没生效——那个工具在**静默直连**
//	没有意愿    = 直连（正常，用户没想接管它）
//
// 中间那种是这套机制唯一的危险故障：它看起来一切正常。所以判据写在一个地方
// （phaseOf），CLI 那行汇总与这张表都从它出发——两个界面对同一份磁盘状态给出
// 不同答案，比其中一个不显示更糟。

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/runtime/takeover"
)

// takeoverPhase 是一个 agent 此刻处在三种状态的哪一种。
type takeoverPhase int

const (
	// phaseDirect 没接管，也没打算接管。正常态，不是错误。
	phaseDirect takeoverPhase = iota
	// phaseActive 接管生效了：磁盘上的现实与用户的意愿一致。
	phaseActive
	// phasePending 用户要求接管，磁盘上却没生效。
	//
	// 这是**唯一的危险故障**：工具会静默直连（请求不经过 newgate），而任何一处
	// 单看都「正常」。典型成因是 start/build 之后漏了一步 `newgate on`。
	phasePending
)

// phaseOf 是三种状态**唯一**的判据（CLI 的 status 行与 web 那张表都走它）。
func phaseOf(s takeover.Status) takeoverPhase {
	switch {
	case s.Active:
		return phaseActive
	case s.Wanted:
		return phasePending
	default:
		return phaseDirect
	}
}

// takeoverConcepts 是接管这一刻的样子。
//
// 报**全部** agent（不只是异常的）：这张表回答的是「谁在走 newgate」，而
// 「claude 在走、opencode 没在走」是同一个答案的两半——只报异常的话，用户看不出
// 「另一个工具是不是也被接管了」。
func takeoverConcepts() ([]view.Concept, error) {
	return []view.Concept{{
		ID:    "runtime.takeover",
		Kind:  view.KindTable,
		Title: i18n.T("Who goes through newgate", nil),
		Data:  takeoverTable(takeover.List()),
	}}, nil
}

// takeoverTable 把一组接管状态摆成一张表。
//
// 它是**纯函数**（喂一组状态进去），因为要钉住的东西正好与机器无关：三态怎么翻、
// 危险的那一种有没有颜色。环境那半边（`takeover.List()` 怎么算出这些行）由
// takeover 包自己的测试守。
func takeoverTable(rows []takeover.Status) view.Table {
	table := view.Table{
		Columns: []view.Column{
			{ID: "agent", Label: i18n.T("agent", nil)},
			{ID: "state", Label: i18n.T("State", nil)},
			// 机制名（shim / config）是机器标记：它同时也是 `newgate doctor`
			// 与 takeover 包里的取值，翻它等于造一套只有界面认识的词汇。
			{ID: "mechanism", Label: i18n.T("Mechanism", nil)},
			{ID: "detail", Label: i18n.T("How", nil)},
		},
		Rows: []map[string]view.Cell{},
	}
	for _, s := range rows {
		text, tone := stateCell(s)
		table.Rows = append(table.Rows, map[string]view.Cell{
			"agent":     {Text: s.Agent},
			"state":     {Text: text, Tone: tone},
			"mechanism": dashIfEmpty(string(s.Mechanism)),
			"detail":    dashIfEmpty(s.Detail),
		})
	}
	return table
}

// stateCell 把三态翻成人话 + 语义色。
func stateCell(s takeover.Status) (string, string) {
	switch phaseOf(s) {
	case phaseActive:
		return i18n.T("taken over", nil), view.ToneOK
	case phasePending:
		// 危险的是**没生效**那一半，所以文案要点明「没生效」，而不是只说
		// 「要求接管」——后者读起来像已经成了。
		return i18n.T("declared but not in effect", nil), view.ToneBad
	default:
		return i18n.T("direct", nil), ""
	}
}

// dashIfEmpty 给空值一个显式的占位符。
//
// 空格子与「这一格是空的」在界面上长得一样，而它们的意思不同：前者读起来像
// 「这里出了问题」或者「没加载出来」。横杠说清楚「这里就是没有」。
func dashIfEmpty(s string) view.Cell {
	if s == "" {
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: s}
}
