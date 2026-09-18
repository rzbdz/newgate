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

// TestAttachFailureRollsBackTheWholeGraph：Attach 阶段失败时，**整张图**都要回滚。
//
// 这条测试是补出来的（2026-09-18）：Attach 是注入边用的第二阶段，判断失败路径时
// 照抄了 Start 那段「回滚范围 = 已经 Start 过的前 i 个」，而 Attach 跑的时候每个
// 组件都早已 Start 完 —— 于是第 i 个之后的组件启动了却永远不 Stop，注入给界面的
// 命令、注册进 gateway 的插件、agentstate 的全局桥全部泄漏。三个组件、第二个的
// Attach 失败，就能看出 c 有没有被停。
func TestAttachFailureRollsBackTheWholeGraph(t *testing.T) {
	var events []string
	_, err := New(fakeLoader{components: []Component{
		{
			Type: "test", Name: "a",
			Start:  func(context.Context, Context) error { events = append(events, "start a"); return nil },
			Attach: func(context.Context, Context) error { events = append(events, "attach a"); return nil },
			Stop:   func(context.Context) error { events = append(events, "stop a"); return nil },
		},
		{
			Type: "test", Name: "b",
			Start:  func(context.Context, Context) error { events = append(events, "start b"); return nil },
			Attach: func(context.Context, Context) error { return context.Canceled },
			Stop:   func(context.Context) error { events = append(events, "stop b"); return nil },
		},
		{
			// 它的 Attach 根本不会被调到（b 先失败），但它**已经 Start 过了**，
			// 所以必须被 Stop —— 这正是原来漏掉的那一段。
			Type: "test", Name: "c",
			Start: func(context.Context, Context) error { events = append(events, "start c"); return nil },
			Stop:  func(context.Context) error { events = append(events, "stop c"); return nil },
		},
	}})
	if err == nil {
		t.Fatal("失败的 Attach 应当让整次装配失败")
	}
	want := []string{"start a", "start b", "start c", "attach a",
		"stop c", "stop b", "stop a"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("事件序列 = %v，应为 %v（Attach 失败必须回滚**整张图**）", events, want)
	}
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
