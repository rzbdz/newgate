package pluginmanager

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 界面那一份开关点（view.go）的断言分两半：**画出来的东西对不对**，以及
// **写下去的东西对不对**。后者最要紧——那是用户按一下就把运行期行为改掉的路径。

func register(t *testing.T, m Manager, module string, sws ...Switch) {
	t.Helper()
	if _, err := m.RegisterSelf(module, sws); err != nil {
		t.Fatal(err)
	}
}

func sw(path, danger string, def bool) Switch {
	s := Switch{Path: path, Title: "title of " + path, Why: "why of " + path, Default: def}
	s.Danger = Danger(danger)
	if s.Danger == DangerFootgun {
		s.TTL = 5 * time.Minute
	}
	return s
}

// TestViewSeparatesFootgunsFromTheRest：footgun 不能出现在那张**可写**卡片里。
//
// 它的写语义要求带时限，而这一版没有让用户选时限的界面；混进去的结果是界面上多了
// 一个按下去必定失败的开关——比不给更糟。所以它单列一张只读卡片。
func TestViewSeparatesFootgunsFromTheRest(t *testing.T) {
	m := start(t)
	register(t, m, "demo",
		sw("demo.kill", "safe", true),
		sw("demo.mode", "quirk", false),
		sw("demo.gun", "footgun", true),
	)

	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]view.Concept{}
	for _, c := range concepts {
		byID[c.ID] = c
	}
	writable, ok := byID["plugin-manager.switches"]
	if !ok {
		t.Fatalf("可写的那张卡片不见了: %+v", concepts)
	}
	if writable.Apply == nil {
		t.Error("开关点卡片该是可写的")
	}
	gun, ok := byID["plugin-manager.footguns"]
	if !ok {
		t.Fatal("footgun 该单列一张卡片")
	}
	if gun.Apply != nil {
		t.Error("footgun 卡片不该可写（这里的写要么失败、要么绕过时限）")
	}
	for _, item := range gun.Data.(switchesData).Items {
		if item.ID != "demo.gun" {
			t.Errorf("footgun 卡片里混进了别的东西: %s", item.ID)
		}
		// 只读就得说清楚**去哪儿改**，不然用户只知道这里点不动。
		if !strings.Contains(item.Why, "newgate plugin") {
			t.Errorf("只读的 footgun 该写明用哪条命令改: %q", item.Why)
		}
	}
	if n := len(writable.Data.(switchesData).Items); n != 2 {
		t.Errorf("可写那张该有两个开关点，实际 %d", n)
	}
}

// TestViewReportsTheEffectiveState：画出来的 `value` 是**当前生效**的状态，而
// 它由出厂态与用户的设定共同决定（kill switch 看 Off 表、mode 看 On 表）。
// 这里画错，用户就会看到一个与 `newgate plugin` 相反的开关。
func TestViewReportsTheEffectiveState(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true), sw("demo.mode", "quirk", false))

	values := func() map[string]bool {
		t.Helper()
		concepts, err := viewConcepts(m)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, item := range concepts[0].Data.(switchesData).Items {
			out[item.ID] = item.Value
		}
		return out
	}

	// 出厂态：kill switch 开着、mode 关着。
	got := values()
	if !got["demo.kill"] || got["demo.mode"] {
		t.Fatalf("出厂态画错了: %v", got)
	}

	// 用户关掉 kill switch：state 里写一条，界面就该跟着变。
	st := store.LoadState()
	if err := SetSwitch(st, sw("demo.kill", "safe", true), false, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if got := values(); got["demo.kill"] {
		t.Errorf("关掉之后界面还说它开着: %v", got)
	}
}

// TestViewApplyWritesWithABaseline：保存走的是「比对基线 → 原子写」那条路。
// 基线过期要能报出**冲突**（而不是把人家的改动盖掉），这正是 web 与 CLI 同时改
// 同一份 state.json 时的现场。
func TestViewApplyWritesWithABaseline(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true))
	concepts, err := viewConcepts(m)
	if err != nil {
		t.Fatal(err)
	}
	c := concepts[0]
	base := c.Data.(switchesData).Base

	rev, err := c.Apply(json.RawMessage(`{"demo.kill":false}`), base)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if rev == base {
		t.Error("写完该给出新基线（前端续着改不用刷新页面）")
	}
	if !Off(store.LoadState(), "demo.kill") {
		t.Error("state 里没有写进「关掉」这条设定")
	}

	// 拿**旧的**基线再写一次：这就是「命令行在你看页面的时候改了同一个文件」。
	// 必须报冲突并把两边原文都带上，绝不覆盖。
	_, err = c.Apply(json.RawMessage(`{"demo.kill":true}`), base)
	var conflict *view.Conflict
	if !errors.As(err, &conflict) {
		t.Fatalf("基线过期该报冲突，实际 %v", err)
	}
	if conflict.Yours == "" || conflict.Theirs == "" {
		t.Error("冲突要把两边原文都带上（用户才知道自己丢的是什么）")
	}
	if conflict.Path == "" || !strings.HasSuffix(conflict.Path, "state.json") {
		t.Errorf("冲突要点名是哪份文件: %q", conflict.Path)
	}
}

// TestViewApplyRejectsAnUnknownSwitch：界面手里的开关点已经不在了（换了产物、
// 模块被关掉）。**报错**而不是跳过：静默跳过的那天，用户以为按下的开关生效了。
func TestViewApplyRejectsAnUnknownSwitch(t *testing.T) {
	m := start(t)
	register(t, m, "demo", sw("demo.kill", "safe", true))
	concepts, _ := viewConcepts(m)
	_, err := concepts[0].Apply(json.RawMessage(`{"demo.gone":true}`), store.Revision(paths.StateFile()))
	if err == nil {
		t.Fatal("不认识的开关点该报错")
	}
	if !strings.Contains(err.Error(), "demo.gone") {
		t.Errorf("报错要点名是哪一个: %v", err)
	}
}
