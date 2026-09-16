// Package component is the small runtime component kernel. It knows only named,
// typed capabilities and component lifecycle; gateway, agents, hooks, and CLI
// extensions are ordinary capability values defined outside this package.
package component

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

type cardinality uint8

const (
	single cardinality = iota
	many
)

type capabilitySpec struct {
	name        string
	valueType   reflect.Type
	cardinality cardinality
}

// Capability is a typed name shared by providers and consumers.
type Capability[T any] struct{ spec capabilitySpec }

func One[T any](name string) Capability[T]  { return newCapability[T](name, single) }
func Many[T any](name string) Capability[T] { return newCapability[T](name, many) }

func newCapability[T any](name string, count cardinality) Capability[T] {
	return Capability[T]{spec: capabilitySpec{
		name:        name,
		valueType:   reflect.TypeOf((*T)(nil)).Elem(),
		cardinality: count,
	}}
}

type Requirement struct {
	spec     capabilitySpec
	optional bool
}

func Need[T any](capability Capability[T]) Requirement {
	return Requirement{spec: capability.spec}
}

func Optional[T any](capability Capability[T]) Requirement {
	return Requirement{spec: capability.spec, optional: true}
}

type Provision struct {
	spec  capabilitySpec
	value any
}

func Provide[T any](capability Capability[T], value T) Provision {
	return Provision{spec: capability.spec, value: value}
}

// Component may both provide and consume capabilities. Start runs after all
// providers required by the component have started; Stop runs in reverse.
type Component struct {
	Name     string
	Requires []Requirement
	Provides []Provision
	Start    func(context.Context, Context) error
	Stop     func(context.Context) error
}

type Release func() error

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

type Loader interface {
	Load() ([]Component, error)
}

type Context struct{ values map[string][]any }

func Get[T any](ctx Context, capability Capability[T]) (T, bool) {
	if capability.spec.cardinality != single {
		panic("component: Get called with many capability " + capability.spec.name)
	}
	var zero T
	values := ctx.values[capability.spec.name]
	if len(values) == 0 {
		return zero, false
	}
	value, ok := values[0].(T)
	return value, ok
}

func MustGet[T any](ctx Context, capability Capability[T]) T {
	value, ok := Get(ctx, capability)
	if !ok {
		panic("component: capability unavailable: " + capability.spec.name)
	}
	return value
}

func GetAll[T any](ctx Context, capability Capability[T]) []T {
	if capability.spec.cardinality != many {
		panic("component: GetAll called with single capability " + capability.spec.name)
	}
	values := ctx.values[capability.spec.name]
	out := make([]T, 0, len(values))
	for _, value := range values {
		if typed, ok := value.(T); ok {
			out = append(out, typed)
		}
	}
	return out
}

type Manager struct {
	components []Component
	context    Context
	started    int
	stopOnce   sync.Once
}

func New(loaders ...Loader) (*Manager, error) {
	return NewContext(context.Background(), loaders...)
}

func NewContext(ctx context.Context, loaders ...Loader) (*Manager, error) {
	var components []Component
	for _, loader := range loaders {
		loaded, err := loader.Load()
		if err != nil {
			return nil, err
		}
		components = append(components, loaded...)
	}
	ordered, values, err := resolve(components)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		components: ordered,
		context:    Context{values: values},
	}
	for i, component := range manager.components {
		if component.Start != nil {
			if err := component.Start(ctx, manager.context); err != nil {
				// Start may have registered some capabilities before failing.
				// Stop must therefore tolerate partial initialization.
				manager.started = i + 1
				rollbackErr := manager.Stop(ctx)
				if rollbackErr != nil {
					return nil, fmt.Errorf("start component %s: %w; rollback: %v",
						component.Name, err, rollbackErr)
				}
				return nil, fmt.Errorf("start component %s: %w", component.Name, err)
			}
		}
		manager.started = i + 1
	}
	return manager, nil
}

func Must(loaders ...Loader) *Manager {
	manager, err := New(loaders...)
	if err != nil {
		panic(fmt.Sprintf("component: load failed: %v", err))
	}
	return manager
}

func (m *Manager) Context() Context { return m.context }

func (m *Manager) ComponentNames() []string {
	names := make([]string, 0, len(m.components))
	for _, component := range m.components {
		names = append(names, component.Name)
	}
	return names
}

func (m *Manager) Stop(ctx context.Context) error {
	var first error
	m.stopOnce.Do(func() {
		for i := m.started - 1; i >= 0; i-- {
			component := m.components[i]
			if component.Stop == nil {
				continue
			}
			if err := component.Stop(ctx); err != nil && first == nil {
				first = fmt.Errorf("stop component %s: %w", component.Name, err)
			}
		}
	})
	return first
}

func resolve(components []Component) ([]Component, map[string][]any, error) {
	byName := make(map[string]int, len(components))
	specs := make(map[string]capabilitySpec)
	providers := make(map[string][]int)
	values := make(map[string][]any)
	for i, component := range components {
		if component.Name == "" {
			return nil, nil, fmt.Errorf("component name is required")
		}
		if _, exists := byName[component.Name]; exists {
			return nil, nil, fmt.Errorf("duplicate component %s", component.Name)
		}
		byName[component.Name] = i
		for _, provision := range component.Provides {
			if err := validateSpec(specs, provision.spec); err != nil {
				return nil, nil, fmt.Errorf("component %s: %w", component.Name, err)
			}
			if isNil(provision.value) {
				return nil, nil, fmt.Errorf("component %s provides nil %s",
					component.Name, provision.spec.name)
			}
			if !reflect.TypeOf(provision.value).AssignableTo(provision.spec.valueType) {
				return nil, nil, fmt.Errorf("component %s provides %s as %T, want %s",
					component.Name, provision.spec.name, provision.value, provision.spec.valueType)
			}
			providers[provision.spec.name] = append(providers[provision.spec.name], i)
			values[provision.spec.name] = append(values[provision.spec.name], provision.value)
		}
	}
	for name, indexes := range providers {
		if specs[name].cardinality == single && len(indexes) > 1 {
			return nil, nil, fmt.Errorf("capability %s has multiple providers: %s",
				name, componentList(components, indexes))
		}
	}

	edges := make([]map[int]bool, len(components))
	indegree := make([]int, len(components))
	for consumer, component := range components {
		for _, requirement := range component.Requires {
			if err := validateSpec(specs, requirement.spec); err != nil {
				return nil, nil, fmt.Errorf("component %s: %w", component.Name, err)
			}
			indexes := providers[requirement.spec.name]
			if len(indexes) == 0 && !requirement.optional {
				return nil, nil, fmt.Errorf("component %s requires missing capability %s",
					component.Name, requirement.spec.name)
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
		return nil, nil, fmt.Errorf("component capability dependency cycle")
	}
	return ordered, values, nil
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

func validateSpec(known map[string]capabilitySpec, spec capabilitySpec) error {
	if spec.name == "" {
		return fmt.Errorf("capability name is required")
	}
	if prior, ok := known[spec.name]; ok {
		if prior.valueType != spec.valueType || prior.cardinality != spec.cardinality {
			return fmt.Errorf("capability %s declared inconsistently", spec.name)
		}
		return nil
	}
	known[spec.name] = spec
	return nil
}

func componentList(components []Component, indexes []int) string {
	names := make([]string, 0, len(indexes))
	for _, index := range indexes {
		names = append(names, components[index].Name)
	}
	return fmt.Sprintf("%v", names)
}
