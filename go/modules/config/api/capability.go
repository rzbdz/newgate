package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
)

// ExtraRole 是模块可追加的语义档位；别名让端口使用者无需依赖配置实现包。
type ExtraRole = domain.ExtraRole

// Step 是解析成功后候选链中的一步。
type Step = resolve.Step

// Skip 解释某个候选为何没有进入最终链，用于可观测性而不是控制流。
type Skip = resolve.Skip

// RoleProvider 允许模块向配置层贡献动态档位，同时保留来源供冲突诊断。
type RoleProvider interface {
	Source() string
	Roles() ([]ExtraRole, error)
}

// RoleWatchProvider 是 RoleProvider 的可选能力，用于声明哪些文件变化后需要重载。
type RoleWatchProvider interface {
	WatchFiles() []string
}

// Config 是配置模块对外的最小端口。消费者只能注册扩展，
// 不能绕过 store/resolve 边界直接修改配置内部状态。
type Config interface {
	RegisterRoleProvider(RoleProvider) (modules.Release, error)
}

// Capability 是配置端口的唯一身份；配置实现只能有一个，避免多份状态分叉。
var Capability = modules.One[Config]("config")
