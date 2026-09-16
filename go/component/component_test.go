package component

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type fakeLoader struct{ components []Component }

func (loader fakeLoader) Load() ([]Component, error) { return loader.components, nil }

func TestManagerInjectsCapabilitiesAndReversesLifecycle(t *testing.T) {
	service := One[string]("service")
	var events []string
	manager, err := New(fakeLoader{components: []Component{
		{
			Name: "consumer", Requires: []Requirement{Need(service)},
			Start: func(ctx Context) error {
				events = append(events, "start consumer "+MustGet(ctx, service))
				return nil
			},
			Stop: func(context.Context) error {
				events = append(events, "stop consumer")
				return nil
			},
		},
		{
			Name: "provider", Provides: []Provision{Provide(service, "ready")},
			Start: func(Context) error {
				events = append(events, "start provider")
				return nil
			},
			Stop: func(context.Context) error {
				events = append(events, "stop provider")
				return nil
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"start provider",
		"start consumer ready",
		"stop consumer",
		"stop provider",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestManagerRejectsInvalidCapabilityGraphs(t *testing.T) {
	service := One[string]("service")
	tests := []struct {
		name       string
		components []Component
		want       string
	}{
		{
			name: "missing",
			components: []Component{{
				Name: "consumer", Requires: []Requirement{Need(service)},
			}},
			want: "requires missing capability service",
		},
		{
			name: "duplicate single provider",
			components: []Component{
				{Name: "a", Provides: []Provision{Provide(service, "a")}},
				{Name: "b", Provides: []Provision{Provide(service, "b")}},
			},
			want: "multiple providers",
		},
		{
			name: "cycle",
			components: func() []Component {
				a := One[string]("a")
				b := One[string]("b")
				return []Component{
					{Name: "a", Requires: []Requirement{Need(b)}, Provides: []Provision{Provide(a, "a")}},
					{Name: "b", Requires: []Requirement{Need(a)}, Provides: []Provision{Provide(b, "b")}},
				}
			}(),
			want: "dependency cycle",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(fakeLoader{components: tt.components})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestManyCapabilityInjectsAllProviders(t *testing.T) {
	hooks := Many[string]("hooks")
	manager, err := New(fakeLoader{components: []Component{
		{Name: "one", Provides: []Provision{Provide(hooks, "one")}},
		{Name: "two", Provides: []Provision{Provide(hooks, "two")}},
		{
			Name: "consumer", Requires: []Requirement{Need(hooks)},
			Start: func(ctx Context) error {
				if got := GetAll(ctx, hooks); !reflect.DeepEqual(got, []string{"one", "two"}) {
					t.Fatalf("hooks = %v", got)
				}
				return nil
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = manager.Stop(context.Background())
}

func TestFailedComponentParticipatesInRollback(t *testing.T) {
	var events []string
	_, err := New(fakeLoader{components: []Component{
		{
			Name: "provider",
			Start: func(Context) error {
				events = append(events, "start provider")
				return nil
			},
			Stop: func(context.Context) error {
				events = append(events, "stop provider")
				return nil
			},
		},
		{
			Name: "partial",
			Start: func(Context) error {
				events = append(events, "start partial")
				return context.Canceled
			},
			Stop: func(context.Context) error {
				events = append(events, "stop partial")
				return nil
			},
		},
	}})
	if err == nil {
		t.Fatal("failed Start should reject manager")
	}
	want := []string{"start provider", "start partial", "stop partial", "stop provider"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}
