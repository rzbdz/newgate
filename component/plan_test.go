package component

import (
	"context"
	"strings"
	"testing"
)

// The whole value of the blueprint (Resolve) is one sentence: **start nothing**.
//
// Why it deserves its own pin: breaking it turns no other test red -- the graph is still right,
// the order is still right, the only symptom is "asked for a graph and the whole world got
// initialized" (ports came up, the global registry was modified, the second spec hits already
// registered on the spot). That is what the architecture diagram tool hit on 2026-09-20.

type seed struct {
	capabilityName string
	provided       bool
	starts         *int
}

func (s seed) component(name string, requires ...Requirement) Component {
	c := Component{Name: name, Type: "test", Requires: requires}
	if s.provided {
		c.Provides = []Provision{Provide(NewCapability[struct{}](s.capabilityName), struct{}{})}
	}
	if s.starts != nil {
		n := s.starts
		c.Start = func(context.Context, Context) error { *n++; return nil }
	}
	return c
}

type fixedLoader []Component

func (f fixedLoader) Load() ([]Component, error) { return f, nil }

// TestResolveDoesNotStartAnything is the reason this layer exists.
func TestResolveDoesNotStartAnything(t *testing.T) {
	starts := 0
	s := seed{capabilityName: "leaf", provided: true, starts: &starts}
	plan, err := Resolve(fixedLoader{
		s.component("leaf"),
		s.component("user", Need(NewCapability[struct{}]("leaf"))),
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if starts != 0 {
		t.Fatalf("Resolve 启动了 %d 个组件——图纸不该有生命周期", starts)
	}
	if got := plan.Names(); len(got) != 2 || got[0] != "leaf" || got[1] != "user" {
		t.Fatalf("顺序不对（被依赖的要在前）: %v", got)
	}
}

// TestPlanOrderMatchesStartOrder: the blueprint and the real start **must** be in the same
// order, otherwise every inference drawn from the blueprint (who is after whom, who gets the
// port first) is false. This pins the two implementations together.
func TestPlanOrderMatchesStartOrder(t *testing.T) {
	// The declaration order is deliberately reversed (dependents first), so that "is the order
	// right" is a real question.
	comps := []Component{
		{Name: "mid", Type: "test", Requires: []Requirement{Need(NewCapability[struct{}]("leaf"))}},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(NewCapability[struct{}]("leaf"), struct{}{})}},
	}
	plan, err := Resolve(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := New(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop(context.Background()) }()

	fromPlan, fromStart := plan.Names(), mgr.ComponentNames()
	if strings.Join(fromPlan, ",") != strings.Join(fromStart, ",") {
		t.Fatalf("图纸顺序 %v 与启动顺序 %v 不一致——两份实现开始漂了", fromPlan, fromStart)
	}
}

// TestResolveStillValidates: the blueprint starts nothing, but **skips no validation** -- a
// Need with a missing provider should error here (that is exactly what "resolve really passes"
// means).
func TestResolveStillValidates(t *testing.T) {
	_, err := Resolve(fixedLoader{
		{Name: "user", Type: "test", Requires: []Requirement{Need(NewCapability[struct{}]("nothing-provides-this"))}},
	})
	if err == nil {
		t.Fatal("缺 provider 的 Need 必须在解析期就报错")
	}
}

// TestDependenciesReportsBothKindsOfEdge: the one distinction most worth seeing in review --
// Need is a hard edge (broken means it will not start), Optional is a weak edge (broken just
// means no entry), and **an Optional nobody provides** is exactly the answer to "what did this
// distribution not install".
func TestDependenciesReportsBothKindsOfEdge(t *testing.T) {
	plan, err := Resolve(fixedLoader{
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(NewCapability[struct{}]("leaf"), struct{}{})}},
		{Name: "user", Type: "test", Requires: []Requirement{
			Need(NewCapability[struct{}]("leaf")),
			Optional(NewCapability[struct{}]("ui")),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := plan.Dependencies()["user"]
	if len(deps) != 2 {
		t.Fatalf("user 该有两条依赖，实际 %+v", deps)
	}
	byCap := map[string]Dependency{}
	for _, d := range deps {
		byCap[d.Capability] = d
	}
	if d := byCap["leaf"]; d.Optional || d.ProvidedBy != "leaf" {
		t.Errorf("leaf 该是硬边且由 leaf 提供: %+v", d)
	}
	if d := byCap["ui"]; !d.Optional || d.ProvidedBy != "" {
		t.Errorf("ui 该是弱边且没人提供（没装界面）: %+v", d)
	}
}

// TestPlanComponentsIsACopy: whoever gets the blueprint must not be able to modify the copy the
// framework holds.
func TestPlanComponentsIsACopy(t *testing.T) {
	plan, err := Resolve(fixedLoader{{Name: "leaf", Type: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Components()
	got[0].Name = "tampered"
	if plan.Names()[0] != "leaf" {
		t.Error("Components() 给出去的是内部切片——调用方能改坏图纸")
	}
}

// stalePlan is a Loader where "the component set is unchanged but an edge changed": the
// components it hands over match the real assembly, but the blueprint is old.
type stalePlan struct {
	components []Component
	order      []string
}

func (s stalePlan) Load() ([]Component, error) { return s.components, nil }

func (s stalePlan) PrecomputedOrder() []string { return s.order }

// TestPrecomputedPlanRejectsAStaleOrder locks down the 2026-11-18 fix.
//
// The scene: an already-assembled dependency flips from Optional to Need (or a new Need is
// added) while the generated list is not regenerated -- **not one component in the set
// changes**, the old validation compared only sets, so it let it through and started a consumer
// that comes before its provider, in the old order. That path is the default path of every CLI,
// so "the provider starts first", the one hard guarantee, had nothing guarding it at the time.
//
// A validation that only silently gives the wrong order is worse than no validation: the graph
// looks good.
func TestPrecomputedPlanRejectsAStaleOrder(t *testing.T) {
	leaf := NewCapability[struct{}]("leaf")
	comps := []Component{
		{Name: "user", Type: "test", Requires: []Requirement{Need(leaf)}},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(leaf, struct{}{})}},
	}
	// The name set matches the component table one by one, only the order is reversed -- exactly
	// the shape of a stale list.
	_, err := Resolve(stalePlan{components: comps, order: []string{"user", "leaf"}})
	if err == nil {
		t.Fatal("图纸里消费者排在提供者前面时必须报错（否则它会拿到零值或 panic）")
	}
	if !strings.Contains(err.Error(), "out of date") || !strings.Contains(err.Error(), "user") {
		t.Fatalf("报错要点名过期的那条边与消费者，实际: %v", err)
	}
}

// TestPrecomputedPlanAcceptsTheRightOrder: for the same graph, when the order is right it must
// still take the fast path -- the fix must not turn the normal path into "errors every time".
func TestPrecomputedPlanAcceptsTheRightOrder(t *testing.T) {
	leaf := NewCapability[struct{}]("leaf")
	comps := []Component{
		{Name: "user", Type: "test", Requires: []Requirement{Need(leaf)}},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(leaf, struct{}{})}},
	}
	plan, err := Resolve(stalePlan{components: comps, order: []string{"leaf", "user"}})
	if err != nil {
		t.Fatalf("顺序正确时不该报错: %v", err)
	}
	if got := plan.Names(); len(got) != 2 || got[0] != "leaf" {
		t.Fatalf("顺序不对: %v", got)
	}
}

// TestPrecomputedPlanRejectsAMissingNeedProvider: the set matches, but some Need port has no
// provider anywhere in the graph (another module was removed and not regenerated) -- also
// stale, and it must also be said out loud.
func TestPrecomputedPlanRejectsAMissingNeedProvider(t *testing.T) {
	_, err := Resolve(stalePlan{
		components: []Component{
			{Name: "user", Type: "test", Requires: []Requirement{Need(NewCapability[struct{}]("gone"))}},
		},
		order: []string{"user"},
	})
	if err == nil {
		t.Fatal("图纸说能起、图上却没人提供这个端口，必须报错")
	}
}

// TestPrecomputedPlanAllowsAnAbsentOptional: an Optional whose provider is absent is **not**
// stale -- that is exactly "this distribution did not install it". The fix must not misjudge it
// as a fault.
func TestPrecomputedPlanAllowsAnAbsentOptional(t *testing.T) {
	if _, err := Resolve(stalePlan{
		components: []Component{
			{Name: "user", Type: "test", Requires: []Requirement{Optional(NewCapability[struct{}]("ui"))}},
		},
		order: []string{"user"},
	}); err != nil {
		t.Fatalf("没人提供的 Optional 是合法装配: %v", err)
	}
}

// TestProvidersAgreesWithGetOnMultipleProviders: with two providers, diagnostics (Providers)
// and runtime (Get) must point at the same one.
//
// Providers used to be last-wins while Get was first-wins: after installing an extra module for
// the same port, the architecture diagram and `newgate st` pointed at a module that was not
// handling any request at all -- two statements each saying their own thing, and nothing went
// red.
func TestProvidersAgreesWithGetOnMultipleProviders(t *testing.T) {
	port := NewCapability[string]("port")
	comps := []Component{
		{Name: "first", Type: "test", Provides: []Provision{Provide(port, "from-first")}},
		{Name: "second", Type: "test", Provides: []Provision{Provide(port, "from-second")}},
	}
	plan, err := Resolve(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Providers()["port"]; got != "first" {
		t.Fatalf("Providers 说 %q 提供 port，但 Get 拿到的是 first", got)
	}
	if all := plan.AllProviders()["port"]; len(all) != 2 || all[0] != "first" || all[1] != "second" {
		t.Fatalf("AllProviders 要按启动顺序列出全部: %v", all)
	}
	got, ok := Get(Context{values: collectValues(plan.Components())}, port)
	if !ok || got != "from-first" {
		t.Fatalf("Get 应该是 first-wins，实际 %q/%v", got, ok)
	}
}

// TestPrecomputedAndResolvedAgreeOnValues: the two ways of producing the graph must give the
// same answer to "who comes first" -- otherwise the same binary behaves differently depending
// on whether it takes the fast path.
func TestPrecomputedAndResolvedAgreeOnValues(t *testing.T) {
	port := NewCapability[string]("port")
	comps := []Component{
		{Name: "a", Type: "test", Provides: []Provision{Provide(port, "a")}},
		{Name: "b", Type: "test", Provides: []Provision{Provide(port, "b")}},
	}
	resolved, err := Resolve(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Resolve(stalePlan{components: comps, order: resolved.Names()})
	if err != nil {
		t.Fatal(err)
	}
	r1, _ := Get(Context{values: resolved.values}, port)
	r2, _ := Get(Context{values: replayed.values}, port)
	if r1 != r2 {
		t.Fatalf("resolved 给 %q、precomputed 给 %q——快路径改变了行为", r1, r2)
	}
}
