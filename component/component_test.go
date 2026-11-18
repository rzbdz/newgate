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
	service := NewCapability[string]("service")
	var events []string
	manager, err := New(fakeLoader{components: []Component{
		{
			Type: "test", Name: "consumer", Requires: []Requirement{Need(service)},
			Start: func(_ context.Context, ctx Context) error {
				events = append(events, "start consumer "+MustGet(ctx, service))
				return nil
			},
			Stop: func(context.Context) error {
				events = append(events, "stop consumer")
				return nil
			},
		},
		{
			Type: "test", Name: "provider", Provides: []Provision{Provide(service, "ready")},
			Start: func(context.Context, Context) error {
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
	service := NewCapability[string]("service")
	tests := []struct {
		name       string
		components []Component
		want       string
	}{
		{
			name: "missing",
			components: []Component{{
				Type: "test", Name: "consumer", Requires: []Requirement{Need(service)},
			}},
			want: "requires missing capability service",
		},
		{
			name: "cycle",
			components: func() []Component {
				a := NewCapability[string]("a")
				b := NewCapability[string]("b")
				return []Component{
					{Type: "test", Name: "a", Requires: []Requirement{Need(b)}, Provides: []Provision{Provide(a, "a")}},
					{Type: "test", Name: "b", Requires: []Requirement{Need(a)}, Provides: []Provision{Provide(b, "b")}},
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
	hooks := NewCapability[string]("hooks")
	manager, err := New(fakeLoader{components: []Component{
		{Type: "test", Name: "one", Provides: []Provision{Provide(hooks, "one")}},
		{Type: "test", Name: "two", Provides: []Provision{Provide(hooks, "two")}},
		{
			Type: "test", Name: "consumer", Requires: []Requirement{Need(hooks)},
			Start: func(_ context.Context, ctx Context) error {
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

// TestOptionalIsAWeakDependency locks down **both** semantics of Optional.
//
// It replaces the Attach-phase test deleted on 2026-09-18
// (TestAttachFailureRollsBackTheWholeGraph). That test guarded "an Attach failure must roll
// back the whole graph" -- Attach was deleted together with Inject (reasoning in the
// component.Optional comment: the ring it was meant to solve disappeared once cli had no
// outbound edges). When you delete a mechanism you must take over **the invariant it carried**,
// otherwise it is a silent downgrade.
//
// Both semantics need a scene of their own:
//
//  1. Provider present -> Optional is an **ordering edge**: the registrant is ordered after the
//     provider, so in its Start it is guaranteed to get the port. This is exactly "whoever
//     injects into others waits for the injected" -- it used to be guaranteed by the Attach
//     phase (run once more after every Start in the graph), now by ordering, which is stronger
//     and simpler.
//  2. Provider absent -> **no edge, no error**: the registrant starts as usual, it just does
//     not get the port.
func TestOptionalIsAWeakDependency(t *testing.T) {
	t.Run("provider present", func(t *testing.T) {
		ui := NewCapability[string]("weak.ui")
		var events []string
		manager, err := New(fakeLoader{components: []Component{
			{
				// Declared first but ordered last: proof that the order comes from that weak dependency
				// edge, not from the declaration position.
				Type: "test", Name: "registrant", Requires: []Requirement{Optional(ui)},
				Start: func(_ context.Context, ctx Context) error {
					value, ok := Get(ctx, ui)
					if !ok {
						// The port should already be usable at Start -- "wait for the injected" is exactly this
						// point.
						t.Fatal("Optional 命中时端口应当已提供（排序边没生效？）")
					}
					events = append(events, "start registrant sees "+value)
					return nil
				},
			},
			{
				Type: "test", Name: "ui", Provides: []Provision{Provide(ui, "surface")},
				Start: func(context.Context, Context) error {
					events = append(events, "start ui")
					return nil
				},
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		_ = manager.Stop(context.Background())
		want := []string{"start ui", "start registrant sees surface"}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("事件序列 = %v，应为 %v（弱依赖命中必须建排序边）", events, want)
		}
	})

	t.Run("provider absent", func(t *testing.T) {
		ui := NewCapability[string]("weak.missing")
		manager, err := New(fakeLoader{components: []Component{
			{
				Type: "test", Name: "registrant", Requires: []Requirement{Optional(ui)},
				Start: func(_ context.Context, ctx Context) error {
					// Absence is not an error, it just means no port: the registrant must still start fine.
					if _, ok := Get(ctx, ui); ok {
						t.Fatal("没有提供者时不该拿到端口")
					}
					return nil
				},
			},
		}})
		if err != nil {
			t.Fatalf("弱依赖缺席不该让装配失败: %v", err)
		}
		_ = manager.Stop(context.Background())
	})
}

func TestFailedComponentParticipatesInRollback(t *testing.T) {
	var events []string
	_, err := New(fakeLoader{components: []Component{
		{
			Type: "test", Name: "provider",
			Start: func(context.Context, Context) error {
				events = append(events, "start provider")
				return nil
			},
			Stop: func(context.Context) error {
				events = append(events, "stop provider")
				return nil
			},
		},
		{
			Type: "test", Name: "partial",
			Start: func(context.Context, Context) error {
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

func TestManagerRejectsTypedNilCapability(t *testing.T) {
	service := NewCapability[*string]("service")
	var value *string
	_, err := New(fakeLoader{components: []Component{{
		Type: "test", Name: "provider", Provides: []Provision{Provide(service, value)},
	}}})
	if err == nil || !strings.Contains(err.Error(), "provides nil service") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartReceivesManagerContext(t *testing.T) {
	key := struct{}{}
	want := "value"
	ctx := context.WithValue(context.Background(), key, want)
	_, err := NewContext(ctx, fakeLoader{components: []Component{{
		Type: "test", Name: "consumer",
		Start: func(ctx context.Context, _ Context) error {
			if got := ctx.Value(key); got != want {
				t.Fatalf("context value = %v", got)
			}
			return nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStartReportsRollbackFailure(t *testing.T) {
	_, err := New(fakeLoader{components: []Component{
		{
			Type: "test", Name: "provider",
			Stop: func(context.Context) error { return context.DeadlineExceeded },
		},
		{
			Type: "test", Name: "consumer",
			Start: func(context.Context, Context) error { return context.Canceled },
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "rollback:") ||
		!strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("err = %v", err)
	}
}

// TestPanickingStartStillRollsBack: a panic and "returning an error" must go through the same
// rollback.
//
// Without recover, a module that has already started keeps its listeners, files and
// registrations, and the caller only sees a stack trace; in tests that is a listening port
// leaking into the next test in the same process. MustGet deliberately **panics** on assembly
// errors, so this path is reachable without any module writing a bug of its own.
func TestPanickingStartStillRollsBack(t *testing.T) {
	var stopped bool
	_, err := New(fakeLoader{components: []Component{
		{
			Type: "test", Name: "provider",
			Start: func(context.Context, Context) error { return nil },
			Stop:  func(context.Context) error { stopped = true; return nil },
		},
		{
			Type: "test", Name: "boom",
			Start: func(context.Context, Context) error { panic("wiring mistake") },
			Stop:  func(context.Context) error { return nil },
		},
	}})
	if err == nil {
		t.Fatal("panicking Start should reject the manager")
	}
	if !strings.Contains(err.Error(), "panicked in Start") {
		t.Fatalf("error should say it was a panic, got: %v", err)
	}
	if !stopped {
		t.Fatal("the already-started component was left running after a panic")
	}
}

// TestRollbackRunsEvenWhenStartContextIsCanceled: rollback must not use the context that may be
// exactly what made Start fail. At process shutdown ctx is already canceled, so cleanup would
// all fail immediately and be swallowed as "stop failed" -- resources stay around, and the log
// only says a pile of stop failed.
func TestRollbackRunsEvenWhenStartContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var stopSawErr error
	_, err := NewContext(ctx, fakeLoader{components: []Component{
		{
			Type: "test", Name: "provider",
			Start: func(context.Context, Context) error { return nil },
			Stop:  func(ctx context.Context) error { stopSawErr = ctx.Err(); return nil },
		},
		{
			Type: "test", Name: "partial",
			Start: func(context.Context, Context) error {
				cancel() // shutdown raced Start
				return context.Canceled
			},
			Stop: func(context.Context) error { return nil },
		},
	}})
	if err == nil {
		t.Fatal("expected failure")
	}
	if stopSawErr != nil {
		t.Fatalf("rollback ran on a canceled context (%v) — cleanup would fail silently", stopSawErr)
	}
}

// catalogSpy records what a delivery handed it; panicOn makes it misbehave.
type catalogSpy struct {
	got     int
	panicOn bool
}

func (c *catalogSpy) SetCatalog(components []Component) {
	if c.panicOn {
		panic("bad consumer")
	}
	c.got = len(components)
}

// TestCatalogDeliveryIsVisibleAndContained: deliverCatalog is the only channel that "writes to
// modules once more after Start", so it must be observable, and its panic must not drag the
// whole process down with it (by then all the listeners are up, and a panic would leak every
// one of them).
func TestCatalogDeliveryIsVisibleAndContained(t *testing.T) {
	capability := NewCapability[*catalogSpy]("spy")
	spy := &catalogSpy{}
	mgr, err := New(fakeLoader{components: []Component{
		{Name: "one", Type: "test"},
		{Name: "spyholder", Type: "test", Provides: []Provision{Provide(capability, spy)}},
		{Name: "two", Type: "test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop(context.Background()) }()
	if spy.got != 3 {
		t.Fatalf("delivery handed %d components, want 3", spy.got)
	}

	boom := &catalogSpy{panicOn: true}
	mgr2, err := New(fakeLoader{components: []Component{
		{Name: "spyholder", Type: "test", Provides: []Provision{Provide(capability, boom)}},
	}})
	if err != nil {
		t.Fatalf("a panicking SetCatalog must not take the process down: %v", err)
	}
	_ = mgr2.Stop(context.Background())
}
