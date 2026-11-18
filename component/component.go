package component

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// capabilitySpec is the runtime identity of a Capability. Generics guarantee type safety at the
// call site, while this reflection information lets the graph-construction phase catch config
// errors such as same name with a different type before any component starts.
type capabilitySpec struct {
	name      string
	valueType reflect.Type
}

// Capability is the typed port identity that providers and consumers both reference. Modules
// depend on this port rather than on a concrete implementation, so implementations can be
// replaced, composed and tested independently.
//
// **Ports do not declare cardinality**. There used to be a One/Many distinction ("only one
// provider" / "multiple allowed"), dropped on 2026-09-17: the framework cannot foresee how many
// implementations a port will have (cli has one today, and there may be a second tomorrow),
// "only one" is the user's constraint, not the framework's, and it certainly should not condemn
// a legal assembly at graph-construction time. With multiple providers Get takes the first in
// start order, and GetAll takes all of them.
//
// Diversity is more commonly expressed not as "one port with multiple providers" but by the
// owner opening registration methods on its own service (see Registry): registration is a
// runtime action with undo, duplicate detection and a lifecycle; Provide is only the static
// declaration of "who binds this port".
type Capability[T any] struct{ spec capabilitySpec }

// NewCapability declares a port identity. The name is a global logical key: ports with the same
// name must have the same type, otherwise the graph-construction phase errors out (see
// validateSpec).
func NewCapability[T any](name string) Capability[T] {
	return Capability[T]{spec: capabilitySpec{
		name:      name,
		valueType: reflect.TypeOf((*T)(nil)).Elem(),
	}}
}

// Name returns the port's logical name. Used by diagnostics and test assertions; business code
// should not branch on it -- that would trade type safety for string comparison, exactly what
// Capability exists to avoid.
func Name[T any](capability Capability[T]) string { return capability.spec.name }

// Requirement describes a port that must be resolved before a component starts, rather than
// holding a service instance. optional only relaxes the "no provider" case; the port type is
// still checked strictly.
//
// There are only two kinds of requirement: **hard dependency** (Need) and **weak dependency**
// (Optional). Before 2026-09-18 there was a third (Inject: an injection edge that creates no
// ordering edge), and it has been deleted -- see the Optional comment for the reasoning.
type Requirement struct {
	spec     capabilitySpec
	optional bool
}

// Name returns the port name this requirement points at, for assembly-time enumeration and
// validation (the kernel does not interpret it, just as it does not interpret the values of
// Type: core provides the table, the product fills in the content).
//
// The reason for adding it is concrete: the product layer wants to assert layering invariants
// like "no component depends on cli", and the fields of Requirement are private and invisible
// from outside. Assertions in the product layer, observation provided by the kernel, is a
// better fit than stuffing invariants into the kernel.
func (r Requirement) Name() string { return r.spec.name }

// Optional reports whether this requirement is optional (established by Optional rather than
// Need).
//
// Together with Name it belongs to assembly-time enumeration: the product layer wants to assert
// layering invariants like "business modules only depend on ui optionally", and the kernel only
// provides the observation, not an interpretation of what "ui" is.
func (r Requirement) Optional() bool { return r.optional }

// Need establishes a **hard dependency**: if the provider is missing the whole graph refuses to
// start, and "I come after the provider" is an explicit ordering edge.
func Need[T any](capability Capability[T]) Requirement {
	return Requirement{spec: capability.spec}
}

// Optional establishes a **weak dependency**: you exist, so I depend on you; you do not, so I
// do not.
//
// The semantics are two rules that hold together:
//
//   - **Ordering**: when a provider exists an edge is created as usual -- I come after it and
//     its Start is guaranteed to finish first. This is where I "wait for it to be ready", and
//     where the sentence "whoever injects into others waits for the injected" lands in the
//     framework (before 2026-09-18 this was covered by Component.Attach, see below).
//   - **Absence means no dependency**: with no provider, **no edge and no error** -- you just
//     do not get the port. So "whether this module is installed" never keeps another module
//     from starting.
//
// The typical user is ui: business modules do Optional(cli) -- with cli installed, hook the
// command up in its Start; without it, skip, and the module itself is missing nothing. The
// reverse direction is still forbidden: **ui depends on no module** (cli's Requires is empty,
// see the ratchet in app/default_test.go).
//
// # 2026-09-18: this used to be Inject + a second Attach phase, deleted
//
// An earlier version, to dodge a **ring that no longer exists**, introduced a third requirement
// `Inject` (optional and not part of ordering) plus the matching `Component.Attach` (an
// injection phase that ran once more after every Start in the graph had run). The ring came
// about like this: injection was an ordering edge back then, and ui **itself also had to depend
// on those modules** to render -- two arrows pointing at each other, so the commands of config
// / runtime / config-hook could never be injected in and were forced to stay inside the UI.
//
// The real fix for the ring was to **cut ui's outbound edges clean** (cli.Requires is now empty
// and the UI only loops over injected callbacks). With the outbound edge gone, the inbound
// Optional(cli) edge cannot form a ring, and the only reason for Inject to exist disappeared
// with it; Attach's only reason was "at Start time ui may not have started yet" -- once the
// ordering edge was restored, it had no reason to exist either.
//
// Keeping them was not free; measured, there were three costs:
//
//   - inject does not take part in ordering => **no guarantee at all about who comes first**.
//     Today the 9 injectors all happen to be ordered after cli, thanks to stable topological
//     sort + alphabetical directory names -- a coincidence, not a mechanism.
//   - The stop order lost its guarantee along with it: breaker was observed stopping **after**
//     cli, i.e. its Stop writes a Release back into a UI ledger that has already stopped.
//     Harmless today (cli.Stop is empty) because convention holds it up.
//   - Every new module author has to read 30 lines of comments before learning "to register
//     with the UI, write Attach, not Start" -- the warning on modules/pluginmanager/command.go
//     is that symptom.
//
// Now there are only two kinds of requirement, each with a single meaning: Need is a hard
// dependency, Optional is a weak one.
func Optional[T any](capability Capability[T]) Requirement {
	return Requirement{spec: capability.spec, optional: true}
}

// Provision binds a concrete value to a port. Binding completes before Start, so dependency
// resolution does not rely on global side effects from component startup.
type Provision struct {
	spec  capabilitySpec
	value any
}

// Name returns the port name this provider binds.
//
// Symmetric with Requirement.Name, and it exists for the same reason: the product layer wants
// to assert layering invariants like "what happens when a module is removed" (see the removal
// matrix in app/matrix_test.go), and the fields of Provision are private. Assertions in the
// product layer, observation provided by the kernel, is a better fit than stuffing invariants
// into the kernel.
func (p Provision) Name() string { return p.spec.name }

// Provide creates a port binding; the dynamic type of the value is checked again at
// graph-construction time.
func Provide[T any](capability Capability[T], value T) Provision {
	return Provision{spec: capability.spec, value: value}
}

// Type is the classification label of a component.
//
// The kernel **defines no concrete value** and does not interpret it -- it is only a
// classification key that must exist and be stable, for assembly-time enumeration and for upper
// layers to display, count and govern by class. **Which classes exist is a product concept, not
// a kernel concept**, so the vocabulary belongs to the product layer; the kernel only provides
// the field. This is the same rule as "module keys are not hard-coded into core": core provides
// the table, the product fills in the content.
//
// It is any value other than the empty string; the kernel knows nothing about the values and
// therefore cannot validate them -- whether a value falls inside the set the product layer
// knows is up to the product layer (usually an assembly test).
type Type string

// Component is a lifecycle node in the runtime dependency graph. Requires/Provides describe the
// static topology; Start/Stop only handle side effects after dependencies are obtained; this
// way dependencies can be validated before execution, and the stop order can be derived
// reliably from the same graph.
type Component struct {
	Name string
	// Type is the classification label, required. It travels with the component definition, so the
	// graph can enumerate every component and its class as soon as it is assembled -- no
	// cooperation from the component, no startup. Things that are only known at runtime cannot go
	// here: that is what a module registers with some owner in Start, not static metadata.
	Type Type
	// Desc is one sentence about **what this module does**; nil = not written (shown as empty in a
	// list, not invented).
	//
	// Why a **closure** and not a string: it has to be translatable, and component literals are
	// built during Load -- there is no guarantee at that point that the language layer is
	// installed (same as view.Title / Concept.Title: registration happens in Start, while
	// evaluation waits until the UI asks). Writing a string would weld the language of "the moment
	// of construction" into it.
	//
	// Why put it on the descriptor instead of having the module register it itself: it is the same
	// kind as Type -- **static metadata**, enumerable as soon as assembly ends, with no
	// cooperation from the module and no startup. Things only known at runtime cannot go here
	// (that is what a module registers with some owner in Start).
	Desc     func() string
	Requires []Requirement
	Provides []Provision
	Start    func(context.Context, Context) error
	Stop     func(context.Context) error
}

// Release undoes one registration's ownership. Returning a handle instead of exposing a global
// Remove ensures a component only releases the contributions it created, and makes Stop
// naturally the inverse of Start.
type Release func() error

// ReleaseAll undoes a set of contributions in the reverse order of registration, keeping the
// same ownership semantics as reverse-order stop.
func ReleaseAll(releases []Release) error {
	var first error
	for i := len(releases) - 1; i >= 0; i-- {
		if releases[i] == nil {
			continue
		}
		if err := releases[i](); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Context is the read-only port table after graph construction, handed to components only
// during Start. It deliberately offers no dynamic writes, to keep the runtime from degenerating
// into a service locator.
type Context struct{ values map[string][]any }

// Get reads a port; the boolean lets the caller handle "no provider" explicitly. With multiple
// providers it returns the first in **start order** -- for all of them use GetAll.
func Get[T any](ctx Context, capability Capability[T]) (T, bool) {
	var zero T
	for _, value := range ctx.values[capability.spec.name] {
		if typed, ok := value.(T); ok {
			return typed, true
		}
	}
	return zero, false
}

// MustGet reads a port that Need has already guaranteed to exist. If the declaration and the
// use disagree it panics immediately, exposing the component's own programming error.
func MustGet[T any](ctx Context, capability Capability[T]) T {
	value, ok := Get(ctx, capability)
	if !ok {
		panic("component: capability unavailable: " + capability.spec.name)
	}
	return value
}

// GetAll reads every contribution of a port, in **start order**, for open extension points like
// plugins and commands. With no provider it returns an empty
// slice rather than nil, so callers need no nil check.
func GetAll[T any](ctx Context, capability Capability[T]) []T {
	values := ctx.values[capability.spec.name]
	out := make([]T, 0, len(values))
	for _, value := range values {
		if typed, ok := value.(T); ok {
			out = append(out, typed)
		}
	}
	return out
}

// Manager owns an already validated and sorted component graph, plus its complete start/stop
// state. Holding only the Manager is enough for a process to get startup-failure rollback and
// normal exit through the same reverse-order semantics.
type Manager struct {
	components []Component
	context    Context
	started    int
	stopOnce   sync.Once
}

// New builds and starts the component graph with a background context, for composition roots
// that need no startup cancellation.
func New(loaders ...Loader) (*Manager, error) {
	return NewContext(context.Background(), loaders...)
}

// NewContext resolves the graph and starts it in **the order the loaders declared**. If any
// Start fails, every node that already entered its lifecycle is stopped immediately in reverse
// order.
//
// The whole process is reported via tracef (see trace.go): how many components were handed over,
// what each declared, how long each took to start. This is the only first-hand material for
// "why modules load this way" -- it exists only during assembly and cannot be asked out of the
// Manager afterwards.
func NewContext(ctx context.Context, loaders ...Loader) (*Manager, error) {
	// Resolving has only one implementation (see the notes in plan.go): starting is
	// **blueprint + lifecycle**, not another path. Written separately once, the two start to
	// drift.
	plan, err := Resolve(loaders...)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		components: plan.components,
		context:    Context{values: plan.values},
	}
	assembledAt := time.Now()
	for i, component := range manager.components {
		began := time.Now()
		if component.Start != nil {
			if err := startComponent(ctx, component, manager.context); err != nil {
				// Start may already have registered extensions or taken resources before returning the
				// error, so the failed node is also in the rollback set; a component's Stop must be able to
				// handle partial initialization.
				manager.started = i + 1
				tracef("%s", i18n.T("start {index}/{total} {name} failed (took {took}): {err}",
					i18n.A{"index": i + 1, "total": len(manager.components),
						"name": component.Name,
						"took": time.Since(began).Round(time.Microsecond), "err": err}))
				rollbackErr := manager.rollback(ctx)
				if rollbackErr != nil {
					return nil, fmt.Errorf("start component %s: %w; rollback: %v",
						component.Name, err, rollbackErr)
				}
				return nil, fmt.Errorf("start component %s: %w", component.Name, err)
			}
		}
		manager.started = i + 1
		tracef("%s", i18n.T("start {index}/{total} {name} done (took {took})",
			i18n.A{"index": i + 1, "total": len(manager.components),
				"name": component.Name,
				"took": time.Since(began).Round(time.Microsecond)}))
	}
	tracef("%s", i18n.T("assembly complete: {count} components, took {took}",
		i18n.A{"count": len(manager.components),
			"took": time.Since(assembledAt).Round(time.Millisecond)}))
	// The list is delivered **after all Starts have returned**: anyone who wants to know who is in
	// the graph only gets the complete answer at this moment. This step is read-only and starts
	// nothing -- see CatalogAware and the three-phase rule in docs/02.
	manager.deliverCatalog()
	return manager, nil
}

// startComponent runs one Start and turns a panic into an error.
//
// A module panicking in Start is a programming error, but it must still unwind the
// same way a returned error does: without this, every already-started module keeps
// its listener, files and registrations, and the caller only sees a stack trace
// (in tests that leaks a listening port into the next test in the process).
// MustGet panics by design for wiring mistakes, so this path is reachable without
// any module being wrong on its own.
func startComponent(ctx context.Context, component Component, services Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panicked in Start: %v", r)
		}
	}()
	return component.Start(ctx, services)
}

// rollback is Stop, but never on a context that may already be the reason Start
// failed: if the caller's ctx is canceled (process shutdown), every cleanup would
// fail immediately and be swallowed as "stop failed". Cleanup gets its own context
// with a bounded budget instead.
func (m *Manager) rollback(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	return m.Stop(ctx)
}

// rollbackTimeout bounds a rollback sweep. A Stop that ignores its context would
// otherwise hang process shutdown with no diagnostic naming the culprit.
const rollbackTimeout = 30 * time.Second

// Context returns a read-only view of the resolved ports, so the composition root can reach the
// final entry service.
func (m *Manager) Context() Context { return m.context }

// ComponentNames returns component names in actual start order, mainly for diagnosing the
// dependency graph.
func (m *Manager) ComponentNames() []string {
	names := make([]string, 0, len(m.components))
	for _, component := range m.components {
		names = append(names, component.Name)
	}
	return names
}

// Components returns copies of the component definitions in start order (including Type), for
// upper layers to enumerate.
//
// A read-only snapshot: what is returned is a shallow copy, so callers cannot modify the one in
// the graph. Its difference from ComponentNames is the classes and dependency declarations. The
// authoritative source for enumeration is here, not "whoever reported itself" -- self-reporting
// misses components that took part in none of those mechanisms, and "took no part" and "forgot
// to register" must look different in the result.
func (m *Manager) Components() []Component {
	out := make([]Component, len(m.components))
	copy(out, m.components)
	return out
}

// Stop runs exactly once and releases components in the reverse of the start direction. Even if
// one Stop fails, the remaining components still clean up, and the first error is returned in
// the end.
//
// Events are reported symmetrically with Start: the stop order is the reverse of the start
// order, and "who stops after whom" is exactly the only place where questions like "will a late
// Release write back into an object that has already stopped" show up.
func (m *Manager) Stop(ctx context.Context) error {
	var first error
	m.stopOnce.Do(func() {
		tracef("%s", i18n.T("stop: releasing {count} components in reverse start order",
			i18n.A{"count": m.started}))
		for i := m.started - 1; i >= 0; i-- {
			component := m.components[i]
			if component.Stop == nil {
				tracef("%s", i18n.T("stop {index}/{total} {name} skipped (no Stop)",
					i18n.A{"index": i + 1, "total": m.started, "name": component.Name}))
				continue
			}
			began := time.Now()
			if err := component.Stop(ctx); err != nil {
				tracef("%s", i18n.T("stop {index}/{total} {name} failed (took {took}): {err}",
					i18n.A{"index": i + 1, "total": m.started, "name": component.Name,
						"took": time.Since(began).Round(time.Microsecond), "err": err}))
				if first == nil {
					first = fmt.Errorf("stop component %s: %w", component.Name, err)
				}
				continue
			}
			tracef("%s", i18n.T("stop {index}/{total} {name} done (took {took})",
				i18n.A{"index": i + 1, "total": m.started, "name": component.Name,
					"took": time.Since(began).Round(time.Microsecond)}))
		}
		tracef("%s", i18n.T("stop: complete", nil))
	})
	return first
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// validateSpec guarantees that a port with the same name has the same type everywhere. The name
// is a global logical key; if two modules use the same name for different things, this must
// fail before any component starts, rather than waiting for some Get to return a zero value
// that fails a type assertion.
func validateSpec(known map[string]capabilitySpec, spec capabilitySpec) error {
	if spec.name == "" {
		return fmt.Errorf("capability name is required")
	}
	if prior, ok := known[spec.name]; ok {
		if prior.valueType != spec.valueType {
			return fmt.Errorf("capability %s declared as %s and %s",
				spec.name, prior.valueType, spec.valueType)
		}
		return nil
	}
	known[spec.name] = spec
	return nil
}

// CatalogAware is implemented by a component that wants the graph's full
// component list delivered once, after every Start has returned.
//
// Why it lives in the kernel: the member list is something the kernel already owns
// (m.components), and the composition root must not name any module.
//
// # Why this is a phase and not a port (and why it is not invisible)
//
// It is delivered after Start, so it is a *write* that happens outside the
// dependency graph — the one thing this framework otherwise refuses to do. Making
// it a port would not work: the list is only complete after the last Start, and a
// port value is bound before any Start. So instead of pretending it is an edge, the
// kernel makes the delivery **visible**: every delivery is traced with the
// component it went to, and any panic in SetCatalog is reported rather than
// unwinding the process (a crash after all Starts would leak every listener).
//
// It stays read-only by contract: a module that registers things from here is
// regressing to the two-phase assembly that was removed on 2026-09-18.
type CatalogAware interface{ SetCatalog([]Component) }

// deliverCatalog hands the full component list to every provider value that
// implements CatalogAware.
//
// Only values a component already Provides are considered: that is the face the
// component chose to expose, and the only channel that does not require the
// composition root to name anyone. Who wants the list implements the interface;
// who does not, does not need it.
func (m *Manager) deliverCatalog() {
	catalog := m.Components()
	for port, values := range m.context.values {
		for _, value := range values {
			aware, ok := value.(CatalogAware)
			if !ok {
				continue
			}
			m.deliverOne(port, value, aware, catalog)
		}
	}
}

// deliverOne traces one delivery and contains a panic from it.
func (m *Manager) deliverOne(port string, value any, aware CatalogAware, catalog []Component) {
	name := fmt.Sprintf("%T", value)
	defer func() {
		if r := recover(); r != nil {
			tracef("%s", i18n.T("catalog: {name} (port {port}) panicked in SetCatalog: {err} "+
				"— delivery skipped; the graph is still up", i18n.A{"name": name, "port": port, "err": r}))
		}
	}()
	aware.SetCatalog(catalog)
	tracef("%s", i18n.T("catalog: delivered {count} components to {name} (port {port})",
		i18n.A{"count": len(catalog), "name": name, "port": port}))
}
