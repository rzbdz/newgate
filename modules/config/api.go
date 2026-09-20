package config

import (
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/resolve"
	"github.com/rzbdz/newgate/modules/config/roleprov"
	"github.com/rzbdz/newgate/modules/config/store"
)

// ExtraRole 是模块可追加的语义档位；别名让端口使用者无需依赖配置实现包。
type ExtraRole = domain.ExtraRole

// 落盘 schema 的四个类型 + profile 的 .kv 解析器：给**发行版**用（配置共享模块
// 在另一台机器上生成配置文件，得先按同一把尺子校验——「用 daemon 自己那个解析器」
// 而不是另写一个差不多的是那边的原话）。
//
// 2026-09-20 之前配置共享住在本仓库里，直接用 store/domain 即可；它搬去发行版之后
// 这条依赖必须走公开面，否则发行版就在 import 内核的实现包——那种依赖在内核重构
// 时会断，而且断在另一个仓库里。别名只做转发、不是新契约：schema 的真相仍在
// domain，解析器的真相仍在 store（同上面 ExtraRole/Step 那几条的写法）。
type (
	Provider  = domain.Provider
	Providers = domain.Providers
	Profile   = domain.Profile
	Binding   = domain.Binding
)

// ParseProfileKV 解析一份 profile 的 .kv 文本（`name = value` 逐行）。
func ParseProfileKV(text string) (*Profile, error) { return store.ParseProfileKV(text) }

// Step 是解析成功后候选链中的一步。
type Step = resolve.Step

// Skip 解释某个候选为何没有进入最终链，用于可观测性而不是控制流。
type Skip = resolve.Skip

// RoleProvider 允许模块向配置层贡献动态档位，同时保留来源供冲突诊断。
// 契约本体在 roleprov（注册表实现所在包）定义，这里只做名字转发。
type RoleProvider = roleprov.RoleProvider

// RoleWatchProvider 是 RoleProvider 的可选能力，用于声明哪些文件变化后需要重载。
type RoleWatchProvider = roleprov.RoleWatchProvider

// Config 是配置模块对外的最小端口。消费者只能注册扩展，
// 不能绕过 store/resolve 边界直接修改配置内部状态。
type Config interface {
	RegisterRoleProvider(RoleProvider) (modules.Release, error)
}

// Capability 是配置端口的唯一身份；配置实现只能有一个，避免多份状态分叉。
var Capability = modules.NewCapability[Config]("config")
