package resolve

import (
	"testing"

	"github.com/rzbdz/newgate/go/internal/core/domain"
)

// ---------- PrimaryBinding ----------

func TestPrimaryBindingReturnsChainHead(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "cheap", Priority: prio(10), Roles: map[string]domain.Candidates{
			"heavy": list("ds/deepseek-chat", "glm/glm-4-plus")}},
	}
	b, ok := PrimaryBinding("heavy", ps, mkProvs("ds", "glm"), "cheap")
	if !ok || b.String() != "ds/deepseek-chat" {
		t.Errorf("PrimaryBinding = %v, %v", b, ok)
	}
}

func TestPrimaryBindingSparseFallsThrough(t *testing.T) {
	// 链头没定义 heavy，链落到下一个 profile，显示名应是那个候选。
	ps := []*domain.Profile{
		{Name: "sparse", Priority: prio(5), Roles: map[string]domain.Candidates{}},
		{Name: "full", Priority: prio(10), Roles: map[string]domain.Candidates{
			"heavy": one("glm", "glm-4-plus")}},
	}
	b, ok := PrimaryBinding("heavy", ps, mkProvs("glm"), "sparse")
	if !ok || b.String() != "glm/glm-4-plus" {
		t.Errorf("PrimaryBinding = %v, %v", b, ok)
	}
}

func TestPrimaryBindingNoCandidate(t *testing.T) {
	ps := []*domain.Profile{{Name: "x", Roles: map[string]domain.Candidates{}}}
	if _, ok := PrimaryBinding("heavy", ps, mkProvs(), "x"); ok {
		t.Error("没有任何候选时应 ok=false")
	}
}

// ---------- ResolveRequest ----------

func TestResolveRequest_TierName(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"heavy": one("ds", "deepseek-chat")}}}
	steps, _, tier := ResolveRequest("heavy", "a", ps, mkProvs("ds"), Opts{Active: "a"})
	if tier != "heavy" {
		t.Errorf("tier = %q", tier)
	}
	eq(t, names(steps), "a:ds/deepseek-chat")
}

func TestResolveRequest_ConcreteModelRoutesToTier(t *testing.T) {
	ps := []*domain.Profile{
		{Name: "ds", Roles: map[string]domain.Candidates{
			"heavy": one("ds", "deepseek-chat"),
			"light": one("ds", "deepseek-flash")}},
	}
	steps, _, tier := ResolveRequest("deepseek-chat", "ds", ps, mkProvs("ds"), Opts{Active: "ds"})
	if tier != "heavy" {
		t.Errorf("tier = %q，应为 heavy", tier)
	}
	eq(t, names(steps), "ds:ds/deepseek-chat")
}

func TestResolveRequest_ConcreteModelMovedToFront(t *testing.T) {
	// heavy 链是 [deepseek-chat, glm-4-plus]；客户端点名 glm-4-plus，
	// 它应该被挪到最前，deepseek-chat 跟在后面。
	ps := []*domain.Profile{
		{Name: "a", Roles: map[string]domain.Candidates{
			"heavy": list("ds/deepseek-chat", "glm/glm-4-plus")}},
	}
	steps, _, tier := ResolveRequest("glm-4-plus", "a", ps, mkProvs("ds", "glm"), Opts{Active: "a"})
	if tier != "heavy" {
		t.Errorf("tier = %q", tier)
	}
	eq(t, names(steps), "a:glm/glm-4-plus", "a:ds/deepseek-chat")
}

func TestResolveRequest_UnknownModel(t *testing.T) {
	ps := []*domain.Profile{{Name: "a", Roles: map[string]domain.Candidates{
		"heavy": one("ds", "deepseek-chat")}}}
	steps, _, tier := ResolveRequest("no-such-model", "a", ps, mkProvs("ds"), Opts{Active: "a"})
	if tier != "" || len(steps) != 0 {
		t.Errorf("未知模型应返回空链和空 tier，得到 tier=%q steps=%v", tier, names(steps))
	}
}

// TestResolveRequest_AmbiguousModelPrefersMid 一对多反查的优先级（2026-09-09
// 实抓回归）：glm-5.3 同时绑 heavy 和 mid（常见配置），客户端发来的名字
// 大多来自 sonnet 槽（主循环/分类器/总结），必须解析成 mid——按 Roles
// 原序命中 heavy 会把 mid 请求硬说成 heavy，下游按档位做决定的插件全部
// 失真（现场：分类器被 claude-bg 跳过，27 秒一条）。
func TestResolveRequest_AmbiguousModelPrefersMid(t *testing.T) {
	ps := []*domain.Profile{{Name: "glm", Priority: prio(20), Roles: map[string]domain.Candidates{
		"heavy": list("glm/glm-5.3", "ds/claude-opus"),
		"mid":   list("glm/glm-5.3", "ds/deepseek-chat"),
	}}}
	steps, _, tier := ResolveRequest("glm-5.3", "glm", ps, mkProvs("glm", "ds"), Opts{Active: "glm"})
	if tier != "mid" {
		t.Fatalf("同名填 heavy+mid 应解析成 mid，实际 %q", tier)
	}
	// 链也用 mid 的：点名模型在头，后面跟 mid 的候选（deepseek-chat），
	// 不是 heavy 的 claude-opus。
	eq(t, names(steps), "glm:glm/glm-5.3", "glm:ds/deepseek-chat")
}

// TestRealNameRoleOrderIsPermutation 优先序必须是 Roles 的重排——漏一个
// 档位会让那个档位独占的模型永远解析不出 tier。
func TestRealNameRoleOrderIsPermutation(t *testing.T) {
	if len(realNameRoleOrder) != len(domain.Roles) {
		t.Fatalf("realNameRoleOrder 与 Roles 数量不一致: %v vs %v", realNameRoleOrder, domain.Roles)
	}
	seen := map[string]bool{}
	for _, r := range realNameRoleOrder {
		if seen[r] {
			t.Errorf("重复档位: %s", r)
		}
		seen[r] = true
		if !domain.IsRole(r) {
			t.Errorf("未知档位: %s", r)
		}
	}
	for _, r := range domain.Roles {
		if !seen[r] {
			t.Errorf("漏了档位: %s", r)
		}
	}
}

func TestResolveRequest_IsRoleIsExact(t *testing.T) {
	// 一个恰好叫 "light" 的模型名会被当成档位名——这是有意的约定。
	if !domain.IsRole("light") {
		t.Fatal("light 应是档位名")
	}
	if domain.IsRole("deepseek-chat") {
		t.Fatal("deepseek-chat 不是档位名")
	}
}
