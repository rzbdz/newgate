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