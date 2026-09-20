package breaker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/view"
)

// TestHealthTableCellsMatchColumns 是这张表的形状棘轮。
//
// 格子是按**列 ID** 索引的（见 view.Table 的注释：行是 map，不是数组），于是
// 键名写错不会报错——前端按列去取，取不到就是空白。一张少了半列的表看起来
// 完全正常，只有对着终端里的 `newgate breaker` 逐格比才会发现。
//
// 同一个理由适用 tone：它不是 view.Tone* 里的词时，前端一律当没给（不着色），
// 于是「这条摘了」在网页上是一片灰。两种错都静默，所以在这里拦。
func TestHealthTableCellsMatchColumns(t *testing.T) {
	b, _ := clocked()
	trip(t, b, "relay", "slow")                            // 摘牌：State=open
	b.Report("relay", "fresh", Input{Kind: KindConnError}) // 只计数：Fails=1

	concepts, err := healthConcepts(b)
	if err != nil {
		t.Fatalf("贡献健康表出错: %v", err)
	}
	if len(concepts) != 1 {
		t.Fatalf("该恰好贡献一张表，实际 %d 个概念", len(concepts))
	}
	c := concepts[0]
	if c.Kind != view.KindTable {
		t.Fatalf("Kind 该是 %q，实际 %q", view.KindTable, c.Kind)
	}
	if c.Apply != nil {
		t.Fatal("这张表是可写的——它报的是 daemon 内存里的状态，没有「写回去」这回事")
	}

	table, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data 该是 view.Table，实际 %T", c.Data)
	}
	if len(table.Columns) == 0 {
		t.Fatal("一列都没有——表头没了，前端画不出东西")
	}

	columns := map[string]bool{}
	for _, col := range table.Columns {
		if col.ID == "" || col.Label == "" {
			t.Errorf("列缺 ID 或 Label: %+v（ID 是格子的索引键，Label 才是给人看的）", col)
		}
		columns[col.ID] = true
	}

	if len(table.Rows) != 2 {
		t.Fatalf("两个 binding 该有两行，实际 %d 行", len(table.Rows))
	}
	for _, row := range table.Rows {
		for id, cell := range row {
			if !columns[id] {
				t.Errorf("格子 %q 没有对应的列——前端取不到，这一格是空白", id)
			}
			switch cell.Tone {
			case "", view.ToneOK, view.ToneWarn, view.ToneBad:
			default:
				t.Errorf("格子 %q 的 tone=%q 不是已知语义色，前端会当没给（静默不着色）", id, cell.Tone)
			}
			if cell.Text == "" {
				t.Errorf("格子 %q 是空的——该显示「没有」时请显式写一个占位符", id)
			}
		}
	}
}

// TestHealthTableReportsHealthyBindingsToo 锁住「报全部，不只报出事的」。
//
// CLI 的 `newgate breaker` 刻意只报出问题的（终端里没问题的行是噪音），网页
// 这张表刻意全报：一张能停在屏幕上的表，好坏是相对的——一条能用的 binding
// 卡顿到什么程度，只有跟旁边那些比才看得出来。
func TestHealthTableReportsHealthyBindingsToo(t *testing.T) {
	b, _ := clocked()
	trip(t, b, "relay", "slow")

	concepts, _ := healthConcepts(b)
	table := concepts[0].Data.(view.Table)
	if len(table.Rows) != 1 {
		t.Fatalf("只报出事的那些了？实际 %d 行", len(table.Rows))
	}
	if got := table.Rows[0]["binding"].Text; got != "relay/slow" {
		t.Fatalf("binding 那一格该是 provider/model，实际 %q", got)
	}
	// 摘了就该是红的（bad），而不是「有值但没颜色」。
	if tone := table.Rows[0]["state"].Tone; tone != view.ToneBad {
		t.Fatalf("摘牌的 state 该是 %q，实际 %q", view.ToneBad, tone)
	}
}

// TestHealthTableIsEmptyWhenNothingIsKnown 是空白装配的那条：一条 binding 都
// 没记过时，表头照在。空表不是错误——「什么都没出问题」与「这张表坏了」在
// 界面上必须是两种样子。
func TestHealthTableIsEmptyWhenNothingIsKnown(t *testing.T) {
	b, _ := clocked()
	concepts, err := healthConcepts(b)
	if err != nil {
		t.Fatalf("没有 binding 时不该出错: %v", err)
	}
	table := concepts[0].Data.(view.Table)
	if len(table.Rows) != 0 {
		t.Fatalf("空表该没有行，实际 %d", len(table.Rows))
	}
	if len(table.Columns) == 0 {
		t.Fatal("空表也该有表头，否则前端连「这张表在说哪几件事」都显示不出来")
	}
	// 空表要序列化成 `[]`，不是 `null`：nil 切片在 JSON 里是 null，而这一格的
	// 契约是「一个列表」。前端靠 `?? []` 兜住了，但协议不该指望每个消费者都
	// 这么小心。
	raw, err := json.Marshal(table)
	if err != nil {
		t.Fatalf("表序列化失败: %v", err)
	}
	if !strings.Contains(string(raw), `"rows":[]`) {
		t.Fatalf("空表的 rows 该是 []，实际: %s", raw)
	}
}

// trip 把一条 binding 连续打失败到开闸（阈值见策略默认值）。
func trip(t *testing.T, b Breaker, provider, model string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		b.Report(provider, model, Input{Kind: KindConnError})
	}
	if b.Available(provider, model) {
		t.Fatalf("%s/%s 打了两发连接失败还没被摘", provider, model)
	}
}
