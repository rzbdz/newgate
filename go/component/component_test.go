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

// TestOptionalIsAWeakDependency 锁住 Optional 的**两条**语义。
//
// 它取代了 2026-09-18 删掉的 Attach 阶段测试（TestAttachFailureRollsBackTheWholeGraph）。
// 那条测试守的是「Attach 失败要回滚整张图」——Attach 连同 Inject 一起删了（理由见
// component.Optional 的注释：它当初要解的环，随 cli 不再有任何出边而消失）。删掉
// 一个机制的同时得把**它承担的那条不变量**接过来，否则就是静默降级。
//
// 两条语义各自都要有现场：
//
//  1. 提供者在场 → Optional 是一条**排序边**：注册者排在提供者后面，于是它在
//     Start 里一定能拿到端口。这一条正是「注入别人的人要等被注入的人」——它以前
//     由 Attach 阶段保证（全图 Start 完之后再跑一遍），现在由排序保证，更强也更简单。
//  2. 提供者缺席 → **不建边、不报错**：注册者照常启动，只是拿不到端口。
func TestOptionalIsAWeakDependency(t *testing.T) {
	t.Run("provider present", func(t *testing.T) {
		ui := NewCapability[string]("weak.ui")
		var events []string
		manager, err := New(fakeLoader{components: []Component{
			{
				// 声明在前、却排在后面：证明顺序来自那条弱依赖边，而不是声明位置。
				Type: "test", Name: "registrant", Requires: []Requirement{Optional(ui)},
				Start: func(_ context.Context, ctx Context) error {
					value, ok := Get(ctx, ui)
					if !ok {
						// 端口在 Start 时应当已经可用——「等被注入的人」就是这一条。
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
					// 缺席不是错误，只是拿不到端口：注册者必须照常起得来。
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
