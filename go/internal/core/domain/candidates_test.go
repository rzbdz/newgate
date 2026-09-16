package domain

import "testing"

// TestCandidatesForNormalFallsBackToMid 四档化（2026-09-16）的向下兼容：
// normal 是新加的**主力档**，老配置里没有它，缺省时用 mid 顶上。
//
// 为什么不是「整层跳过」：跳过会让没写 normal 的配置在主力档上没有候选
// （链空转、claude 主循环直接 404），而四档化之前主循环用的就是 mid 那一档
// 的资源——语义上就该跟 mid 一样。
//
// 2026-09-16 起这条等价关系**降级成候选里的一个引用**（@mid）而不是「直接
// 返回 mid 的候选列表」：模块贡献的槽位键（omo-sisyphus）也是同一种东西，
// 让它们在解析器里走完全同一条路（就地展开、去重、环检测），比在
// CandidatesFor 里再养一套「去别的键翻候选人」的旁路更不容易出错。
// 链层面的结果完全一样——见下面 Resolve 的断言。
func TestCandidatesForNormalFallsBackToMid(t *testing.T) {
	mid := Candidates{{Provider: "p", Model: "sonnet-class"}}
	heavy := Candidates{{Provider: "p", Model: "fable-class"}}
	normal := Candidates{{Provider: "p", Model: "opus-class"}}

	old := &Profile{Roles: map[string]Candidates{"heavy": heavy, "mid": mid}}
	if got := old.CandidatesFor("normal"); len(got) != 1 || got[0].Ref != "mid" {
		t.Errorf("没写 normal 时应等价于 mid（引用形态），实际 %v", got)
	}
	// 引用走到底是真正干活的地方要的结果
	if b, ok := old.Resolve("normal"); !ok || b.Model != "sonnet-class" {
		t.Errorf("normal 解析出来该是 mid 的候选，实际 %v %v", b, ok)
	}

	// 写了 normal 就用自己的，跟 mid 无关
	withNormal := &Profile{Roles: map[string]Candidates{
		"heavy": heavy, "normal": normal, "mid": mid}}
	if got := withNormal.CandidatesFor("normal"); got[0].Model != "opus-class" {
		t.Errorf("写了 normal 应该用自己的，实际 %v", got)
	}
	// 其余档位不受这条规则影响
	if got := withNormal.CandidatesFor("heavy"); got[0].Model != "fable-class" {
		t.Errorf("heavy 被 normal 的规则带跑了: %v", got)
	}
	if got := withNormal.CandidatesFor("mid"); got[0].Model != "sonnet-class" {
		t.Errorf("mid 被改了: %v", got)
	}

	// 连 mid 都没有：退回通配 / fallback 的老路，而不是凭空空转
	bare := &Profile{Roles: map[string]Candidates{"heavy": heavy},
		Fallback: &Binding{Provider: "p", Model: "fallback"}}
	if b, ok := bare.Resolve("normal"); !ok || b.Model != "fallback" {
		t.Errorf("没有 mid 时应走 fallback，实际 %v %v", b, ok)
	}
}

// TestRolesOrder 档位序即能力从高到低——status / doctor / TUI 都按它排，
// 顺手钉死四档化的结果。
func TestRolesOrder(t *testing.T) {
	want := []string{"heavy", "normal", "mid", "light", "vision"}
	if len(Roles) != len(want) {
		t.Fatalf("Roles = %v，想要 %v", Roles, want)
	}
	for i := range want {
		if Roles[i] != want[i] {
			t.Fatalf("Roles = %v，想要 %v", Roles, want)
		}
	}
}
