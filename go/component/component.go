package component

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// cardinality 把“一个实现”和“可组合的一组实现”放进同一套依赖图语义。
// 它不是容器细节：single 会拒绝歧义提供者，many 则保留所有扩展贡献。
type cardinality uint8

const (
	single cardinality = iota
	many
)

// capabilitySpec 是 Capability 的运行时身份。泛型保证调用点类型安全，
// 这份反射信息则让构图阶段能在启动任何组件前发现同名异型等配置错误。
type capabilitySpec struct {
	name        string
	valueType   reflect.Type
	cardinality cardinality
}

// Capability 是提供者与消费者共同引用的有类型端口身份。
// 模块依赖这个端口而不是依赖具体实现，因此实现可以被替换、组合和独立测试。
type Capability[T any] struct{ spec capabilitySpec }

// One 声明只能有一个提供者的端口；重复提供会在构图阶段失败，避免隐式选边。
func One[T any](name string) Capability[T] { return newCapability[T](name, single) }

// Many 声明可由多个组件共同贡献的扩展点，读取顺序服从稳定的组件顺序。
func Many[T any](name string) Capability[T] { return newCapability[T](name, many) }

func newCapability[T any](name string, count cardinality) Capability[T] {
	return Capability[T]{spec: capabilitySpec{
		name:        name,
		valueType:   reflect.TypeOf((*T)(nil)).Elem(),
		cardinality: count,
	}}
}

// Requirement 描述组件启动前必须解析的端口，而不是保存服务实例。
// optional 只放宽“没有提供者”的情况，不放宽端口类型或基数冲突。
type Requirement struct {
	spec     capabilitySpec
	optional bool
}

// Need 建立硬依赖；缺少提供者时整张图拒绝启动。
func Need[T any](capability Capability[T]) Requirement {
	return Requirement{spec: capability.spec}
}

// Optional 建立可选依赖；存在提供者时仍会建立生命周期顺序。
func Optional[T any](capability Capability[T]) Requirement {
	return Requirement{spec: capability.spec, optional: true}
}

// Provision 把一个具体值绑定到端口。绑定会先于 Start 完成，
// 因此依赖解析不依赖组件启动时的全局副作用。
type Provision struct {
	spec  capabilitySpec
	value any
}

// Provide 创建端口绑定；值的动态类型会在构图阶段再次校验。
func Provide[T any](capability Capability[T], value T) Provision {
	return Provision{spec: capability.spec, value: value}
}

// Component 是运行时依赖图中的一个生命周期节点。
// Requires/Provides 描述静态拓扑，Start/Stop 只处理获得依赖后的副作用；
// 这样依赖关系可在执行前验证，停止顺序也能由同一张图可靠推导。
type Component struct {
	Name     string
	Requires []Requirement
	Provides []Provision
	Start    func(context.Context, Context) error
	Stop     func(context.Context) error
}

// Release 撤销一次注册所有权。返回句柄而不是暴露全局 Remove，
// 可以确保组件只释放自己创建的贡献，并让 Stop 自然成为 Start 的逆操作。
type Release func() error

// ReleaseAll 按注册的相反顺序撤销一组贡献，与组件逆序停止保持相同所有权语义。
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

// Loader 在构图前贡献一组组件。它只负责声明组合，不负责自行启动组件，
// 因而所有组件仍由同一个 Manager 统一验证和回滚。
type Loader interface {
	Load() ([]Component, error)
}

// Context 是构图后只读的端口表，只在 Start 阶段交给组件。
// 它刻意不提供动态写入，防止运行期退化为 service locator。
type Context struct{ values map[string][]any }

// Get 读取 single 端口；布尔值让 Optional 的消费者显式处理缺失情况。
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

// MustGet 读取已经由 Need 保证存在的 single 端口。
// 若声明与使用不一致则立即 panic，暴露组件自身的编程错误。
func MustGet[T any](ctx Context, capability Capability[T]) T {
	value, ok := Get(ctx, capability)
	if !ok {
		panic("component: capability unavailable: " + capability.spec.name)
	}
	return value
}

// GetAll 读取 many 端口的全部贡献，用于插件、命令等开放扩展点。
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

// Manager 拥有一张已经验证并排序的组件图，以及它的完整启停状态。
// 进程只需持有 Manager，就能保证启动失败回滚和正常退出使用同一套逆序语义。
type Manager struct {
	components []Component
	context    Context
	started    int
	stopOnce   sync.Once
}

// New 使用后台 context 构建并启动组件图，适合没有启动取消需求的组合根。
func New(loaders ...Loader) (*Manager, error) {
	return NewContext(context.Background(), loaders...)
}

// NewContext 收集组件、验证端口、拓扑排序并依次启动。
// 任一 Start 失败都会立即逆序停止已经进入生命周期的节点。
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
				// Start 可能在报错前已经注册扩展或占用资源，因此失败节点也进入
				// 回滚范围；组件的 Stop 必须能处理部分初始化。
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

// Must 是组合根的 fail-fast 入口；库代码应优先使用 New 返回可处理的错误。
func Must(loaders ...Loader) *Manager {
	manager, err := New(loaders...)
	if err != nil {
		panic(fmt.Sprintf("component: load failed: %v", err))
	}
	return manager
}

// Context 返回已解析端口的只读视图，供组合根访问最终入口服务。
func (m *Manager) Context() Context { return m.context }

// ComponentNames 按实际启动顺序返回组件名，主要用于诊断依赖图。
func (m *Manager) ComponentNames() []string {
	names := make([]string, 0, len(m.components))
	for _, component := range m.components {
		names = append(names, component.Name)
	}
	return names
}