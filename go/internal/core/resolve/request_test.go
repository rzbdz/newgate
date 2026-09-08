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

func TestResolveRequest_IsRoleIsExact(t *testing.T) {
	// 一个恰好叫 "light" 的模型名会被当成档位名——这是有意的约定。
	if !domain.IsRole("light") {
		t.Fatal("light 应是档位名")
	}
	if domain.IsRole("deepseek-chat") {
		t.Fatal("deepseek-chat 不是档位名")
	}
}
