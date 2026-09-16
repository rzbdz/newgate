package api

import (
	modules "github.com/rzbdz/newgate/go/component"
)

// ConfigHooks 是客户端模块写入配置扩展的所有权端口。
// 每次注册都返回 Release，使模块停止时能精确撤销自己的贡献。
type ConfigHooks interface {
	RegisterAgent(*Agent) (modules.Release, error)
	BindTakeover(agentID string, takeover ConfigTakeover) (modules.Release, error)
	RegisterStateField(owner, name string) (modules.Release, error)
}

// AgentCatalog 是运行时和 CLI 的只读客户端目录，
// 与 ConfigHooks 分离后，消费者无法借查询能力修改注册表。
type AgentCatalog interface {
	Get(id string) (*Agent, bool)
	Names() []string
}

// 两个 capability 是同一注册表的读写分面：写端只交给扩展模块，
// 读端交给 runtime 和 CLI，防止查询者获得注册权限。
var (
	ConfigHooksCapability  = modules.One[ConfigHooks]("config-hooks")
	AgentCatalogCapability = modules.One[AgentCatalog]("agent-catalog")
)
