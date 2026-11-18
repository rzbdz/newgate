package component

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
)

// Plan is the **blueprint** of one assembly: the sorted components, what each of them needs,
// and who provides what.
//
// It holds no lifecycle -- not a single Start runs, no port is taken, no file is written, no
// global registry is modified. So it can be evaluated repeatedly (ask the same blueprint ten
// times and it is the same one), and it can be evaluated where **nothing should start**:
// diagnostics, documentation, architecture diagrams, and questions like "what exactly does this
// distribution install".
//
// # Why this distinction deserves a type
//
// Hit while adding the architecture diagram tool on 2026-09-20: to get that "who depends on
// whom" diagram, the only entrance was New(), and New() **starts** the whole graph. So drawing
// the diagram for the second distribution blew up --
//
//	quirk: signature the-model-always-thinks already registered
//
// because the first spec had already started the module once, and it was already in the global
// registry. This is not a flaw in the tool, it is **a layer the framework was missing**:
// resolve and start were welded together, and whoever wanted the first half had to pay for the
// second. The cost of welding them is normally invisible (the composition root really does need
// both), and only shows up when you "just want to ask one question" -- and by then people have
// usually written a second implementation (copying resolve), after which the framework's graph
// and the tool's graph each grow their own way.
type Plan struct {
	components []Component
	values     map[string][]any
}

// Precomputed is a Loader with **its own blueprint**: the components it hands over come from a
// **module set fixed at compile time** (no runtime plugging in), and the start order has
// already been computed at build time.
//
// # Why it exists (2026-09-21)
//
// The user's own words: "logically, running a cli shouldn't trigger assembly... this dependency
// relationship is a compile-time thing, resolving it at runtime does nothing for you, generate
// the dependency relationship at build time. Because it is statically fixed, there is no
// runtime plug-in mechanism."
//
// He was right. The modules installed are fixed at **build time** (chosen by the spec, names
// written into the generated list), and the order is only a function of that set -- so it is a
// compile-time fact. Before this, every `newgate status` and every web backend startup redid
// that work from scratch: build the port table, build the dependency edges, topologically sort,
// check for cycles, and then write the declarations of 24 components and 55 edges into the log.
//
// With this exit installed, all that is left on that path is "lay it out by the blueprint and
// collect the port values from each component's Provides" -- no edges, no sorting, no cycle
// check, none of those log lines.
//
// # So where did validation go
//
// To **build time**, which is exactly where it belongs: tools/distgen runs a real Resolve when
// it generates this order, so a spec that cannot assemble now **fails the build**, instead of
// waiting for someone to type the first command. The three kinds of error -- "duplicate
// component name", "Need port has no provider", "the graph has a cycle" -- are far better
// reported at build time than at runtime: the former is one build away from "who broke which
// line".
//
// When the order and the set disagree (the generated list is stale), this still **errors**:
// that is the one thing that must be caught at runtime, because its symptom (some module did
// not start) is too far from the cause. See planFromOrder.
type Precomputed interface {
	Loader
	// PrecomputedOrder returns component names in start order (dependencies first). nil = this
	// Loader has no blueprint, resolve as usual at runtime.
	PrecomputedOrder() []string
}

// Resolve collects components, validates ports and topologically sorts them, **starting
// nothing**.
//
// New() is exactly "Resolve, then Start one by one", two faces on the same implementation (see
// NewContext): there is only one validation and sort, so they cannot drift.
func Resolve(loaders ...Loader) (*Plan, error) {
	var components []Component
	var precomputed []string
	for i, loader := range loaders {
		loaded, err := loader.Load()
		if err != nil {
			tracef("%s", i18n.T("assembly: loader {index} failed: {err}",
				i18n.A{"index": i + 1, "err": err}))
			return nil, err
		}
		components = append(components, loaded...)
		if p, ok := loader.(Precomputed); ok && len(precomputed) == 0 {
			precomputed = p.PrecomputedOrder()
		}
	}
	if len(precomputed) > 0 {
		// **The build-time computed blueprint**: lay it out, collect the port values, done. See the
		// Precomputed notes.
		return planFromOrder(components, precomputed)
	}
	tracef("%s", i18n.T("assembly: {count} components, no precomputed plan — resolving at run time",
		i18n.A{"count": len(components)}))
	ordered, values, err := resolve(components)
	if err != nil {
		tracef("%s", i18n.T("assembly: graph build failed: {err}", i18n.A{"err": err}))
		return nil, err
	}
	return &Plan{components: ordered, values: values}, nil
}

// planFromOrder produces the blueprint directly from the **build-time computed order**: no
// edges, no sorting, no cycle check.
//
// It does only three things: lay out by name, collect each port's provided values, and check
// that the blueprint matches the source.
//
// **That check is the only validation left here**, and the reason is: the generated list can go
// stale (spec edited, module added but not regenerated), and the symptom of staleness is "some
// module did not start" or "the start order is inexplicable" -- too far from "the list is
// stale", so it must be said plainly at runtime. The other three kinds of error (duplicate
// name, missing port, cycle) are all caught at build time by that real Resolve run in distgen.
//
// # Why check the **edges** too, not just the set (2026-11-18)
//
// When an already-assembled dependency is changed to `Need` (or a new `Need` is added) and the
// list is not regenerated, **not one component in the set changes**: the old validation let it
// through as soon as "the name sets are equal", so it started in the old order -- the consumer
// could come before its provider and MustGet would get a zero value or panic on the spot. This
// path is the whole process's **default path** (every CLI goes through it), so "the order is
// only guaranteed by the generator" is another way of saying nobody guards this invariant. The
// price is one O(edges) scan, and it buys back "the provider always starts first", the one hard
// guarantee.
func planFromOrder(components []Component, order []string) (*Plan, error) {
	if len(order) != len(components) {
		return nil, fmt.Errorf("precomputed plan lists %d components but %d were assembled "+
			"— the generated manifest is out of date (rerun the generator)",
			len(order), len(components))
	}
	byName := make(map[string]Component, len(components))
	for _, c := range components {
		byName[c.Name] = c
	}
	out := make([]Component, 0, len(components))
	values := map[string][]any{}
	providedBy := map[string][]string{}
	seen := make(map[string]bool, len(components))
	for _, name := range order {
		c, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("precomputed plan names %q, which was not assembled "+
				"— the generated manifest is out of date (rerun the generator)", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("precomputed plan names %q twice", name)
		}
		seen[name] = true
		for _, prov := range c.Provides {
			values[prov.spec.name] = append(values[prov.spec.name], prov.value)
			providedBy[prov.spec.name] = append(providedBy[prov.spec.name], c.Name)
		}
		out = append(out, c)
	}
	// Replay the graph's edges against the positions we just assigned. A need whose
	// provider sits later (or is missing) is a stale plan — say so here, by name,
	// instead of letting the consumer quietly receive nil.
	pos := make(map[string]int, len(out))
	for i, c := range out {
		pos[c.Name] = i
	}
	for i, c := range out {
		for _, req := range c.Requires {
			providers := providedBy[req.spec.name]
			if len(providers) == 0 {
				// No provider at all: fatal for Need, fine for Optional. A precomputed
				// plan is only ever generated from a successful Resolve, so a missing
				// Need means the set drifted — same message family as below.
				if req.optional {
					continue
				}
				return nil, fmt.Errorf("precomputed plan: %s needs port %q but nothing "+
					"in the assembled graph provides it — the generated manifest is out "+
					"of date (rerun the generator)", c.Name, req.spec.name)
			}
			for _, provider := range providers {
				j, ok := pos[provider]
				if !ok {
					return nil, fmt.Errorf("precomputed plan: %s needs port %q, provided "+
						"by %s, which is not in the plan — the generated manifest is out "+
						"of date (rerun the generator)", c.Name, req.spec.name, provider)
				}
				// Optional is a weak dependency but still an ordering edge when the
				// provider is present (see Optional's comment), so backwards is stale
				// either way — the module would Start before what it waits for.
				if j >= i {
					return nil, fmt.Errorf("precomputed plan: %s (position %d) needs port "+
						"%q, but its provider %s starts at position %d — the generated "+
						"manifest is out of date (rerun the generator)", c.Name, i,
						req.spec.name, provider, j)
				}
			}
		}
	}
	reportMultipleProviders(providedBy)
	tracef("%s", i18n.T("assembly: precomputed plan used — {count} components, no resolution",
		i18n.A{"count": len(out)}))
	return &Plan{components: out, values: values}, nil
}

// reportMultipleProviders names every port with more than one provider.
//
// Multiple providers are legal (a port declares no cardinality), but the consequence
// is not obvious: Get takes the first in start order, so the others are unreachable
// through it and only visible via GetAll. A distribution that accidentally ships a
// second implementation would otherwise change what some commands hit with nobody
// saying so — one trace line is the cheapest place to make that visible.
func reportMultipleProviders(providedBy map[string][]string) {
	for port, providers := range providedBy {
		if len(providers) < 2 {
			continue
		}
		tracef("%s", i18n.T("graph: port {port} has {count} providers in start order: {providers} "+
			"— Get returns the first; use GetAll for the rest",
			i18n.A{"port": port, "count": len(providers), "providers": strings.Join(providers, ", ")}))
	}
}

// MustResolve is the fail-fast version of Resolve (in the composition root and tests the graph
// must be good).
func MustResolve(loaders ...Loader) *Plan {
	plan, err := Resolve(loaders...)
	if err != nil {
		panic("component: resolve failed: " + err.Error())
	}
	return plan
}

// Components returns the components in **start order**: dependencies first.
func (p *Plan) Components() []Component {
	out := make([]Component, len(p.components))
	copy(out, p.components)
	return out
}

// Names is the name version of the above (for diagnostic output).
func (p *Plan) Names() []string {
	out := make([]string, 0, len(p.components))
	for _, c := range p.components {
		out = append(out, c.Name)
	}
	return out
}

// Providers says who provides each port: capability → component name.
//
// Ports **do not declare cardinality** (see the Capability comment), so a port may have several
// providers. What is reported here is **the one Get will return** -- the first in start order,
// consistent with what the agent actually receives. It used to be last-wins here (later one
// overwrites the earlier), while Get is first-wins: install a second provider for the same port
// and the architecture diagram and the `st` diagnostics point at a module that is **not
// handling any request at all**. When two statements describe the same thing, they must not
// each say their own.
//
// For every provider use AllProviders (in diagnostics, "how many are competing for this port").
func (p *Plan) Providers() map[string]string {
	out := map[string]string{}
	for _, c := range p.components {
		for _, prov := range c.Provides {
			if _, seen := out[prov.Name()]; !seen {
				out[prov.Name()] = c.Name
			}
		}
	}
	return out
}

// AllProviders is **every** provider of each port, in start order (Get takes the first).
//
// Why it exists: multiple providers is not an error (ports do not declare cardinality), but
// "how many there are and who they are" must be observable -- otherwise when a distribution
// installs an extra module for the same port, the symptom is "some commands suddenly went to
// another implementation", and nothing anywhere says so.
func (p *Plan) AllProviders() map[string][]string {
	out := map[string][]string{}
	for _, c := range p.components {
		for _, prov := range c.Provides {
			out[prov.Name()] = append(out[prov.Name()], c.Name)
		}
	}
	return out
}

// Dependencies are the edges of this graph: component name → the ports it needs (sorted by
// name, for stable output).
//
// Each requirement carries **whether it is Optional** -- the difference between the two is
// exactly the one thing to look at during review: if a Need is broken the assembly will not
// start (hard edge), if an Optional is broken it only means "this feature has no entry" (weak
// edge, see component.Optional).
type Dependency struct {
	Capability string
	ProvidedBy string // 空 = 这次装配里没人提供（Optional 的常见下场）
	Optional   bool
}

func (p *Plan) Dependencies() map[string][]Dependency {
	providers := p.Providers()
	out := map[string][]Dependency{}
	for _, c := range p.components {
		for _, req := range c.Requires {
			out[c.Name] = append(out[c.Name], Dependency{
				Capability: req.Name(),
				ProvidedBy: providers[req.Name()],
				Optional:   req.Optional(),
			})
		}
		sort.Slice(out[c.Name], func(i, j int) bool {
			return out[c.Name][i].Capability < out[c.Name][j].Capability
		})
	}
	return out
}
