package runtime

import (
	"testing"

	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/runtime/takeover"
)

// TestTakeoverPhaseIsThreeStates 钉住三态判据。
//
// 它**不是**布尔值，而这一点是这张表存在的理由：「接管没接管」听起来像二选一，
// 实际上有三种现场，中间那种（用户要求接管、磁盘上却没生效）是这个机制唯一的
// 危险故障——工具会**静默直连**，请求根本不经过 newgate，而任何一处单看都正常。
//
// CLI 那行汇总与 web 这张表都从 phaseOf 出发，所以判据错了是两个界面一起错
// （这正是要在这里钉住的原因）。
func TestTakeoverPhaseIsThreeStates(t *testing.T) {
	cases := []struct {
		name string
		s    takeover.Status
		want takeoverPhase
	}{
		{"磁盘装了 = 生效", takeover.Status{Wanted: true, Active: true}, phaseActive},
		{"要求了但没装上 = 危险的那种", takeover.Status{Wanted: true, Active: false}, phasePending},
		{"没要求 = 直连（正常）", takeover.Status{Wanted: false, Active: false}, phaseDirect},

		// 用户要求过 off、磁盘上却没放开（释放失败：多用户下常见的权限坑，
		// 见 CLAUDE.md §3.1）。**今天算「生效中」**——判据是「磁盘说了算」。
		//
		// 这里记的是**现状**，不是主张：CLI 那行汇总的注释把这一种也称作
		// 「两个对称故障」之一（「放开过但文件还指着代理」），可代码从没为它
		// 亮过灯。两种读法都说得通（磁盘是现实 / 意愿没兑现也是故障），而它
		// 会改变用户看到的东西，所以**不在这里单方面定**：先按现状钉住，
		// 要改就两个界面一起改（phaseOf 是它们共同的判据）。
		{"没要求但磁盘还装着 = 按现状算生效", takeover.Status{Wanted: false, Active: true}, phaseActive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := phaseOf(c.s); got != c.want {
				t.Errorf("phaseOf(%+v) = %v，期望 %v", c.s, got, c.want)
			}
		})
	}
}

// TestDangerousPhaseIsRed 是上一条的可见面：三种状态在界面上必须**长得不一样**，
// 而中间那种必须是红的。
//
// 灰的或者没颜色的「声明了但没生效」在屏幕上是看不见的——而它恰恰是那个「你
// 以为 claude 在走 newgate，其实它直连」的现场。
func TestDangerousPhaseIsRed(t *testing.T) {
	pending := takeover.Status{Agent: "claude", Wanted: true, Active: false}
	_, tone := stateCell(pending)
	if tone != view.ToneBad {
		t.Fatalf("「声明了却没生效」该是 %q（唯一的危险故障），实际 %q", view.ToneBad, tone)
	}
	if _, tone := stateCell(takeover.Status{Agent: "claude", Wanted: true, Active: true}); tone != view.ToneOK {
		t.Errorf("生效中该是 %q，实际 %q", view.ToneOK, tone)
	}
	// 直连是正常态：**不着色**。给它上色会让一张全是「直连」的表看起来像故障。
	if text, tone := stateCell(takeover.Status{Agent: "opencode"}); tone != "" {
		t.Errorf("直连不该着色（那是正常态），实际 tone=%q text=%q", tone, text)
	}
}

// TestTakeoverTableShape 是这张表的形状棘轮（与 breaker/plugin-manager 那两张
// 同一条）：格子按列 ID 索引，键名写错不会报错——前端取不到就是空白。
func TestTakeoverTableShape(t *testing.T) {
	table := takeoverTable([]takeover.Status{
		{Agent: "claude", Mechanism: takeover.MechShim, Wanted: true, Active: true, Detail: "/x/bin/claude → newgate"},
		{Agent: "opencode", Mechanism: takeover.MechConfig},
	})

	if len(table.Columns) == 0 {
		t.Fatal("没有列——表头没了，前端画不出东西")
	}
	columns := map[string]bool{}
	for _, col := range table.Columns {
		if col.ID == "" || col.Label == "" {
			t.Errorf("列缺 ID 或 Label: %+v（ID 是格子的索引键，Label 才是给人看的）", col)
		}
		columns[col.ID] = true
	}
	if len(table.Rows) != 2 {
		t.Fatalf("两个 agent 该有两行，实际 %d", len(table.Rows))
	}
	for _, row := range table.Rows {
		for id, cell := range row {
			if !columns[id] {
				t.Errorf("格子 %q 没有对应的列——前端取不到，这一格是空白", id)
			}
			switch cell.Tone {
			case "", view.ToneOK, view.ToneWarn, view.ToneBad:
			default:
				t.Errorf("格子 %q 的 tone=%q 不是已知语义色（前端会当没给）", id, cell.Tone)
			}
			if cell.Text == "" {
				t.Errorf("格子 %q 是空的——该显式写一个占位符（见 dashIfEmpty）", id)
			}
		}
	}
}

// TestTakeoverTableKeepsTheHeaderWhenEmpty：一个 agent 都没接管时表头照在。
//
// 空表不是错误——「谁都没走 newgate」与「这张表坏了」在界面上必须是两种样子。
func TestTakeoverTableKeepsTheHeaderWhenEmpty(t *testing.T) {
	table := takeoverTable(nil)
	if len(table.Rows) != 0 {
		t.Fatalf("空表该没有行，实际 %d", len(table.Rows))
	}
	if len(table.Columns) == 0 {
		t.Fatal("空表也该有表头，否则前端连「这张表在说哪几件事」都显示不出来")
	}
}
