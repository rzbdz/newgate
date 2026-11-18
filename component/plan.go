package component

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
)

// Plan is the **blueprint** of one assembly: the components in start order, what each of them
// needs, and who provides what.
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

// Loader contributes a set of components. It only declares the composition and does not start
// anything, so all components are still validated and rolled back uniformly by the same Manager.
//
// # The slice IS the start order (2026-11-18)
//
// A provider appears before every consumer that Needs or Optionals it. This is not a hint, not a
// preference, and not something the framework is free to rearrange: a Loader whose slice is not
// a valid order **fails assembly**, naming the component and the port.
//
// Why the order is part of the declaration and not something the framework computes:
//
// The module set is fixed at **build time** (chosen by a spec, compiled into the binary), so the
// start order is a function of that set alone -- a compile-time fact. The user's own words
// (2026-09-21): "logically, running a cli shouldn't trigger assembly... generate the dependency
// relationship at build time. Because it is statically fixed, there is no runtime plug-in
// mechanism." Before 2026-11-18 every `newgate status` and every web backend startup rebuilt the
// port table, rebuilt the edges, topologically sorted and checked for cycles -- work whose answer
// could not have changed since the binary was linked.
//
// So there is **one** mechanism, not two exits: whoever writes a declaration computes the order
// once with Ordered and hands it over (the build-time generator freezes it into a committed
// table; testkit computes it from the components the test passed). Resolve then only **verifies**.
type Loader interface {
	Load() ([]Component, error)
}

// Static is a Loader over a fixed slice: the declaration is already in start order.
//
// It exists so "I have the table, hand it over" needs no per-package three-line type. A caller
// whose components are in **arbitrary** order goes through Ordered first -- that is the only
// place an order is computed, and it is deliberately not hidden inside Static.
type Static []Component

// Load implements Loader.
func (s Static) Load() ([]Component, error) { return s, nil }

// Resolve loads every loader's components, validates the assembly and verifies that the declared
// order really is one, **starting nothing**.
//
// New() is exactly "Resolve, then Start one by one" (see NewContext): there is only one
// validation, so the two cannot drift.
//
// # What it does not do
//
// It does not sort. There is no topological sort here and no cycle check, because there is
// nothing to sort: the order arrived with the declaration. A dependency cycle cannot exist in a
// linear list -- its symptom is "a provider is not earlier than its consumer", which is exactly
// what the edge replay below reports, by name.
//
// # Which errors live here, and which live in Ordered
//
// Three kinds of error are about the **declaration** and are reported in both paths, because
// both need them: a duplicate component name, an empty name/Type, a port declared with two types,
// a nil provision. The rest -- "a Need has no provider", "the order contradicts an edge" -- can
// only be judged against an order, and both paths judge them.
//
// The one thing Resolve cannot tell you is whether a slice is the *canonical* order (the one
// Ordered would produce). That property is worth locking, and it is locked in the test of
// whoever froze the order -- see manifest/order_test.go in the distribution.
func Resolve(loaders ...Loader) (*Plan, error) {
	var components []Component
	for i, loader := range loaders {
		loaded, err := loader.Load()
		if err != nil {
			tracef("%s", i18n.T("assembly: loader {index} failed: {err}",
				i18n.A{"index": i + 1, "err": err}))
			return nil, err
		}
		components = append(components, loaded...)
	}
	values, err := validate(components)
	if err != nil {
		return nil, err
	}
	providedBy := providersOf(components)
	if err := checkOrder(components, providedBy); err != nil {
		return nil, err
	}
	reportMultipleProviders(providedBy)
	tracef("%s", i18n.T("assembly: {count} components, in declared order",
		i18n.A{"count": len(components)}))
	return &Plan{components: components, values: values}, nil
}

// validate checks every component and every port on its own, and returns each port's values **in
// declaration order**.
//
// It is shared by Resolve and Ordered: a duplicate name or a port declared as two types is wrong
// whether or not someone recomputed the order, and two copies of this check is how the two paths
// would start to disagree about what a valid graph is.
func validate(components []Component) (map[string][]any, error) {
	byName := make(map[string]bool, len(components))
	specs := make(map[string]capabilitySpec)
	values := make(map[string][]any)
	for _, component := range components {
		if component.Name == "" {
			return nil, fmt.Errorf("component name is required")
		}
		// The class key is required, but **the value is not validated**: the kernel does not know
		// which classes there are (see the Type comment). The correctness of values is covered by
		// the assembly tests in the product layer.
		if component.Type == "" {
			return nil, fmt.Errorf("component %s: type is required", component.Name)
		}
		if byName[component.Name] {
			return nil, fmt.Errorf("duplicate component %s", component.Name)
		}
		byName[component.Name] = true
		for _, provision := range component.Provides {
			if err := validateSpec(specs, provision.spec); err != nil {
				return nil, fmt.Errorf("component %s: %w", component.Name, err)
			}
			if isNil(provision.value) {
				return nil, fmt.Errorf("component %s provides nil %s",
					component.Name, provision.spec.name)
			}
			if !reflect.TypeOf(provision.value).AssignableTo(provision.spec.valueType) {
				return nil, fmt.Errorf("component %s provides %s as %T, want %s",
					component.Name, provision.spec.name, provision.value, provision.spec.valueType)
			}
			values[provision.spec.name] = append(values[provision.spec.name], provision.value)
		}
		for _, requirement := range component.Requires {
			if err := validateSpec(specs, requirement.spec); err != nil {
				return nil, fmt.Errorf("component %s: %w", component.Name, err)
			}
		}
	}
	return values, nil
}

// checkOrder replays every requirement against the positions it was declared at.
//
// A consumer whose provider sits later (or is missing) is an invalid declaration. This is the
// one hard guarantee the framework gives -- "the provider always starts first" -- so it is
// checked on **every** path, including the precomputed one that used to trust the generator.
func checkOrder(components []Component, providedBy map[string][]string) error {
	pos := make(map[string]int, len(components))
	for i, c := range components {
		pos[c.Name] = i
	}
	for i, c := range components {
		for _, req := range c.Requires {
			providers := providedBy[req.spec.name]
			if len(providers) == 0 {
				if req.optional {
					// A weak dependency nobody provides is not an error and creates no ordering
					// constraint -- "you exist, so I depend on you; you do not, so I do not".
					tracef("%s", i18n.T("  optional dependency absent: {consumer} needs {provider} "+
						"— no provider, skipped (no effect on startup)",
						i18n.A{"consumer": c.Name, "provider": req.spec.name}))
					continue
				}
				return fmt.Errorf("component %s requires missing capability %s", c.Name, req.spec.name)
			}
			for _, provider := range providers {
				j, ok := pos[provider]
				if !ok {
					return fmt.Errorf("component %s requires %s, provided by %s, which is not in the graph",
						c.Name, req.spec.name, provider)
				}
				// A provider may also consume its own port (the framework tolerates that and
				// creates no edge for it, so there is nothing to order).
				if j == i {
					continue
				}
				if j > i {
					return fmt.Errorf("component %s (position %d) requires %s, but its provider %s "+
						"is declared at position %d — a provider must come before its consumer",
						c.Name, i, req.spec.name, provider, j)
				}
			}
		}
	}
	return nil
}

// Ordered is **the only implementation of "compute a valid start order"**: it validates the
// components, builds the dependency edges and returns a stable topological order.
//
// # Who calls it
//
// Whoever **writes** a declaration, and only them:
//
//   - tools/distgen, at build time, freezing each spec's order into the generated manifest;
//   - testkit, for the ad-hoc graphs of unit tests ("me + my dependency chain");
//   - a composition root that assembles its table at hand rather than from a generated one.
//
// It is deliberately **not** on the assembly path: Resolve never calls it, and a Loader that
// hands over an unsorted slice is an error rather than a save (see Loader).
//
// Ties are ordered by original declaration position, so extension points and diagnostic output
// are reproducible.
//
// # Why one function and not a method on Plan
//
// Because the order has to be computed where the declaration is written, which is usually a
// different program (the generator) or a different package (a test). It takes components and
// returns components -- no Manager, no Plan, no side effects -- so the thing that computes the
// order and the thing that verifies it cannot grow apart.
func Ordered(components []Component) ([]Component, error) {
	if _, err := validate(components); err != nil {
		return nil, err
	}
	providers := make(map[string][]int)
	for i, component := range components {
		for _, provision := range component.Provides {
			providers[provision.spec.name] = append(providers[provision.spec.name], i)
		}
	}

	edges := make([]map[int]bool, len(components))
	indegree := make([]int, len(components))
	edgeTotal := 0
	for consumer, component := range components {
		for _, requirement := range component.Requires {
			indexes := providers[requirement.spec.name]
			if len(indexes) == 0 {
				// No edge: an absent Optional is legal, and an absent Need is reported by the
				// checkOrder at the end (with the position it would have had, which is the more
				// useful sentence of the two).
				continue
			}
			for _, provider := range indexes {
				if provider == consumer {
					continue
				}
				if edges[provider] == nil {
					edges[provider] = make(map[int]bool)
				}
				if !edges[provider][consumer] {
					edges[provider][consumer] = true
					indegree[consumer]++
					edgeTotal++
				}
			}
		}
	}

	var ready []int
	for i := range components {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	sort.Ints(ready)
	ordered := make([]Component, 0, len(components))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		ordered = append(ordered, components[current])
		var next []int
		for dependent := range edges[current] {
			next = append(next, dependent)
		}
		sort.Ints(next)
		for _, dependent := range next {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Ints(ready)
			}
		}
	}
	if len(ordered) != len(components) {
		// The scene of the ring: name the components still stuck -- saying only "there is a ring"
		// leaves the next step to manual bisection.
		var stuck []string
		for i, component := range components {
			if indegree[i] > 0 {
				stuck = append(stuck, component.Name)
			}
		}
		sort.Strings(stuck)
		tracef("%s", i18n.T("graph: dependency cycle, components still stuck: {names}",
			i18n.A{"names": strings.Join(stuck, " ")}))
		return nil, fmt.Errorf("component capability dependency cycle")
	}
	// The last word is the same verification every assembly gets: an order is only worth
	// something if a Need it cannot satisfy is reported **here**, where the order is being
	// written down. Without this line the generator would happily freeze a spec that cannot
	// start, and the failure would surface on somebody's first command instead of the build.
	if err := checkOrder(ordered, providersOf(ordered)); err != nil {
		return nil, err
	}
	tracef("%s", i18n.T("graph: {count} components, {edges} ordering edges — order computed",
		i18n.A{"count": len(components), "edges": edgeTotal}))
	return ordered, nil
}

// providersOf maps each port to its provider names, in the slice's order.
//
// Shared by both entries so "who provides what" is derived in one place: the two of them
// disagreeing about a provider would be the same class of bug as two orderings.
func providersOf(components []Component) map[string][]string {
	out := make(map[string][]string, len(components))
	for _, c := range components {
		for _, prov := range c.Provides {
			out[prov.spec.name] = append(out[prov.spec.name], c.Name)
		}
	}
	return out
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
	ProvidedBy string // empty = nobody provides it in this assembly (the usual fate of an Optional)
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
