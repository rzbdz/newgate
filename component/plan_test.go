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

// TestResolveDoesNotStartAnything is the reason this layer exists.
func TestResolveDoesNotStartAnything(t *testing.T) {
	starts := 0
	s := seed{capabilityName: "leaf", provided: true, starts: &starts}
	plan, err := Resolve(Static{
		s.component("leaf"),
		s.component("user", Need(NewCapability[struct{}]("leaf"))),
	})
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if starts != 0 {
		t.Fatalf("Resolve started %d components -- the blueprint must have no lifecycle", starts)
	}
	if got := plan.Names(); len(got) != 2 || got[0] != "leaf" || got[1] != "user" {
		t.Fatalf("wrong order (the declaration's order must be kept): %v", got)
	}
}

// TestResolveKeepsTheDeclaredOrder: the order is a **fact of the declaration**, so Resolve is
// not allowed a preference of its own. Two independent components must come out exactly as they
// went in, including the "wrong" alphabetical order.
func TestResolveKeepsTheDeclaredOrder(t *testing.T) {
	plan, err := Resolve(Static{
		{Name: "zulu", Type: "test"},
		{Name: "alpha", Type: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Names(), ","); got != "zulu,alpha" {
		t.Fatalf("Resolve rearranged an order it had no reason to touch: %s", got)
	}
}

// TestPlanOrderMatchesStartOrder: the blueprint and the real start **must** be in the same
// order, otherwise every inference drawn from the blueprint (who is after whom, who gets the
// port first) is false.
func TestPlanOrderMatchesStartOrder(t *testing.T) {
	leaf := NewCapability[struct{}]("leaf")
	comps := []Component{
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(leaf, struct{}{})}},
		{Name: "mid", Type: "test", Requires: []Requirement{Need(leaf)}},
	}
	plan, err := Resolve(Static(comps))
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := New(Static(comps))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop(context.Background()) }()

	fromPlan, fromStart := plan.Names(), mgr.ComponentNames()
	if strings.Join(fromPlan, ",") != strings.Join(fromStart, ",") {
		t.Fatalf("the blueprint order %v disagrees with the start order %v -- the two implementations are drifting", fromPlan, fromStart)
	}
}

// TestResolveStillValidates: the blueprint starts nothing, but **skips no validation** -- a
// Need with a missing provider should error here (that is exactly what "resolve really passes"
// means).
func TestResolveStillValidates(t *testing.T) {
	_, err := Resolve(Static{
		{Name: "user", Type: "test", Requires: []Requirement{Need(NewCapability[struct{}]("nothing-provides-this"))}},
	})
	if err == nil {
		t.Fatal("a Need whose provider is missing must error at resolve time")
	}
}

// TestResolveRejectsAConsumerBeforeItsProvider is the 2026-11-18 fix, now the only mechanism.
//
// The scene it came from: a module's dependency flips from Optional to Need (or a new Need is
// added) while the frozen table is not regenerated. **Not one name in the set changes**, so a
// set-only validation let it through and started a consumer before its provider -- MustGet then
// returns a zero value or panics. Because the order now *is* the declaration, this is the one
// error the framework must never leave unsaid.
func TestResolveRejectsAConsumerBeforeItsProvider(t *testing.T) {
	leaf := NewCapability[struct{}]("leaf")
	_, err := Resolve(Static{
		{Name: "user", Type: "test", Requires: []Requirement{Need(leaf)}},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(leaf, struct{}{})}},
	})
	if err == nil {
		t.Fatal("a consumer declared before its provider must error")
	}
	for _, want := range []string{"user", "leaf", "before"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the consumer, the provider and the rule, got: %v", err)
		}
	}
}

// TestResolveAllowsASelfRequirement: a component may consume a port it provides itself. The
// framework makes no edge out of it, so there is no order to violate -- and the requirement is
// satisfied by the component's own provision.
func TestResolveAllowsASelfRequirement(t *testing.T) {
	port := NewCapability[struct{}]("self")
	if _, err := Resolve(Static{{
		Name:     "one",
		Type:     "test",
		Requires: []Requirement{Need(port)},
		Provides: []Provision{Provide(port, struct{}{})},
	}}); err != nil {
		t.Fatalf("a component may need its own port: %v", err)
	}
}

// TestResolveAllowsAnAbsentOptional: an Optional whose provider is absent is **not** an error --
// that is exactly "this distribution did not install it".
func TestResolveAllowsAnAbsentOptional(t *testing.T) {
	if _, err := Resolve(Static{{
		Name:     "user",
		Type:     "test",
		Requires: []Requirement{Optional(NewCapability[struct{}]("ui"))},
	}}); err != nil {
		t.Fatalf("an Optional nobody provides is a valid assembly: %v", err)
	}
}

// TestDependenciesReportsBothKindsOfEdge: the one distinction most worth seeing in review --
// Need is a hard edge (broken means it will not start), Optional is a weak edge (broken just
// means no entry), and **an Optional nobody provides** is exactly the answer to "what did this
// distribution not install".
func TestDependenciesReportsBothKindsOfEdge(t *testing.T) {
	plan, err := Resolve(Static{
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
		t.Fatalf("user should have two dependencies, got %+v", deps)
	}
	byCap := map[string]Dependency{}
	for _, d := range deps {
		byCap[d.Capability] = d
	}
	if d := byCap["leaf"]; d.Optional || d.ProvidedBy != "leaf" {
		t.Errorf("leaf should be a hard edge provided by leaf: %+v", d)
	}
	if d := byCap["ui"]; !d.Optional || d.ProvidedBy != "" {
		t.Errorf("ui should be a weak edge that nobody provides (no UI installed): %+v", d)
	}
}

// TestPlanComponentsIsACopy: whoever gets the blueprint must not be able to modify the copy the
// framework holds.
func TestPlanComponentsIsACopy(t *testing.T) {
	plan, err := Resolve(Static{{Name: "leaf", Type: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Components()
	got[0].Name = "tampered"
	if plan.Names()[0] != "leaf" {
		t.Error("Components() hands out the internal slice -- the caller can corrupt the blueprint")
	}
}

// TestOrderedComputesADependencyOrder: Ordered is the only place an order is **computed**, and
// it is what the build-time generator freezes. Bare consumers must come out after providers,
// and ties must keep the declaration's relative order (stable, so the generated table does not
// churn between runs).
func TestOrderedComputesADependencyOrder(t *testing.T) {
	leaf := NewCapability[struct{}]("leaf")
	comps := []Component{
		{Name: "second", Type: "test"},
		{Name: "user", Type: "test", Requires: []Requirement{Need(leaf)}},
		{Name: "first", Type: "test"},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(leaf, struct{}{})}},
	}
	ordered, err := Ordered(comps)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ordered))
	for _, c := range ordered {
		names = append(names, c.Name)
	}
	want := "second,first,leaf,user"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("Ordered gave %s, want %s -- ties must keep declaration order and the provider must precede its consumer", got, want)
	}
}

// TestOrderedRejectsACycle: a cycle cannot be expressed as an order, so the loudest place to
// report it is where an order is being computed -- the generator (fails the build) rather than
// somebody's first command.
func TestOrderedRejectsACycle(t *testing.T) {
	a, b := NewCapability[struct{}]("a"), NewCapability[struct{}]("b")
	_, err := Ordered([]Component{
		{Name: "a", Type: "test", Requires: []Requirement{Need(b)}, Provides: []Provision{Provide(a, struct{}{})}},
		{Name: "b", Type: "test", Requires: []Requirement{Need(a)}, Provides: []Provision{Provide(b, struct{}{})}},
	})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a cycle must be reported as such, got: %v", err)
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
	plan, err := Resolve(Static(comps))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Providers()["port"]; got != "first" {
		t.Fatalf("Providers says %q provides port, but Get gets first", got)
	}
	if all := plan.AllProviders()["port"]; len(all) != 2 || all[0] != "first" || all[1] != "second" {
		t.Fatalf("AllProviders must list everything in start order: %v", all)
	}
	got, ok := Get(Context{values: plan.values}, port)
	if !ok || got != "from-first" {
		t.Fatalf("Get should be first-wins, got %q/%v", got, ok)
	}
}
