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