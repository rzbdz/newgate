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