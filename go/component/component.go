package component

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// capabilitySpec 是 Capability 的运行时身份。泛型保证调用点类型安全，
// 这份反射信息则让构图阶段能在启动任何组件前发现同名异型等配置错误。
type capabilitySpec struct {
	name      string
	valueType reflect.Type
}

// Capability 是提供者与消费者共同引用的有类型端口身份。
// 模块依赖这个端口而不是依赖具体实现，因此实现可以被替换、组合和独立测试。
//
// **端口不声明基数**。曾经有过 One/Many 之分（"只能有一个提供者" / "允许多个"），
// 2026-09-17 取消：框架没法预知一个端口将来会有几个实现（cli 今天一个，
// 明天就可能有第二个），"只能有一个"是使用者的约束，不是框架的约束，
// 更不该在构图期把合法的装配判死。多提供者时 Get 取声明顺序里的第一个，
// GetAll 取全部。
//
// 多样性更常见的表达方式不是"一个端口多个提供者"，而是 owner 在自己的
// service 上开注册方法（见 Registry）：注册是运行期动作，有撤销、有查重、
// 有生命周期；Provide 只是"这个端口由谁绑定"的静态声明。
type Capability[T any] struct{ spec capabilitySpec }

// NewCapability 声明一个端口身份。名字是全局逻辑键：同名端口必须同型，
// 否则构图阶段直接报错（见 validateSpec）。
func NewCapability[T any](name string) Capability[T] {
	return Capability[T]{spec: capabilitySpec{
		name:      name,
		valueType: reflect.TypeOf((*T)(nil)).Elem(),
	}}
}

// Name 返回端口的逻辑名。诊断输出和测试断言用；业务代码不该拿它做分支——
// 那等于把类型安全换成字符串比较，正是 Capability 想避免的事。
func Name[T any](capability Capability[T]) string { return capability.spec.name }

// Requirement 描述组件启动前必须解析的端口，而不是保存服务实例。
// optional 只放宽"没有提供者"这一种情况，端口类型仍然严格校验。
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

// Type 是组件的分类标签。
//
// 内核**不定义任何具体取值**，也不解释它——它只是一个必须存在的、稳定的分类键，
// 供装配期枚举与上层按类展示、统计、治理使用。**有哪些分类是产品概念，不是内核
// 概念**，所以词汇表归产品层；内核只提供这个字段。这与「模块的键不 hard-code 进
// core」是同一条规矩：core 提供表，产品填内容。
//
// 它是空字符串以外的任意值；内核对取值一无所知，因此校验不了——取值是否落在
// 产品层认识的集合里，由产品层自己兜（通常是一条装配测试）。
type Type string

// Component 是运行时依赖图中的一个生命周期节点。
// Requires/Provides 描述静态拓扑，Start/Stop 只处理获得依赖后的副作用；
// 这样依赖关系可在执行前验证，停止顺序也能由同一张图可靠推导。
type Component struct {
	Name string
	// Type 是分类标签，必填。它跟着组件定义走，所以图一装配就能枚举出全部
	// 组件及其分类——不需要组件配合、不需要启动。需要运行期才知道的东西
	// 不能放这里：那是模块在 Start 时向某个 owner 注册的事，不是静态元数据。
	Type     Type
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

// Get 读取一个端口；布尔值让调用方显式处理"没有提供者"的情况。
// 多个提供者时返回声明顺序里的第一个——要全部请用 GetAll。
func Get[T any](ctx Context, capability Capability[T]) (T, bool) {
	var zero T
	for _, value := range ctx.values[capability.spec.name] {
		if typed, ok := value.(T); ok {
			return typed, true
		}
	}
	return zero, false
}

// MustGet 读取已经由 Need 保证存在的端口。
// 若声明与使用不一致则立即 panic，暴露组件自身的编程错误。
func MustGet[T any](ctx Context, capability Capability[T]) T {
	value, ok := Get(ctx, capability)
	if !ok {
		panic("component: capability unavailable: " + capability.spec.name)
	}
	return value
}

// GetAll 读取端口的全部贡献，顺序与组件声明顺序一致，用于插件、命令这类
// 开放扩展点。没有提供者时返回空切片而非 nil，调用方无需判空。
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

// Components 按启动顺序返回组件定义副本（含 Type），供上层枚举。
//
// 只读快照：返回的是浅拷贝，调用方改不到图里的那一份。它跟 ComponentNames 的区别
// 就是多了分类与依赖声明。枚举的权威来源是这里而不是「谁自报过」——自报会漏掉
// 没参与那些机制的组件，而「没参与」和「忘了注册」在结果上必须长得不一样。
func (m *Manager) Components() []Component {
	out := make([]Component, len(m.components))
	copy(out, m.components)
	return out
}

// Stop 只执行一次，并按启动的反方向释放组件。
// 即使某个 Stop 失败，其余组件仍继续清理，最终返回第一个错误。
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

// resolve 在任何副作用发生前验证端口并生成稳定拓扑顺序。
// 同序候选按原始声明位置排序，使扩展点和诊断输出可复现。
func resolve(components []Component) ([]Component, map[string][]any, error) {
	byName := make(map[string]int, len(components))
	specs := make(map[string]capabilitySpec)
	providers := make(map[string][]int)
	values := make(map[string][]any)
	for i, component := range components {
		if component.Name == "" {
			return nil, nil, fmt.Errorf("component name is required")
		}
		// 分类键必填，但**不校验取值**：内核不认识有哪些分类（见 Type 的注释）。
		// 取值正确性由 app 层的装配测试兜。
		if component.Type == "" {
			return nil, nil, fmt.Errorf("component %s: type is required", component.Name)
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

// validateSpec 保证同名端口处处同型。名字是全局逻辑键，若两个模块用同一个
// 名字指代不同的东西，这里必须在启动任何组件之前失败，而不是等到某次 Get
// 拿到一个类型断言失败的零值。
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
