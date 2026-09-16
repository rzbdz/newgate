package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
)

type RoleProvider interface {
	Source() string
	Roles() ([]configapi.ExtraRole, error)
}

type RoleWatchProvider interface {
	WatchFiles() []string
}

type ConfigHooks interface {
	RegisterAgent(*Agent) error
	BindTakeover(agentID string, takeover ConfigTakeover) error
	RegisterRoleProvider(RoleProvider)
	RegisterStateField(owner, name string) error
}

type AgentCatalog interface {
	Get(id string) (*Agent, bool)
	Names() []string
	StateFieldOwner(name string) (string, bool)
}

var (
	ConfigHooksCapability  = modules.One[ConfigHooks]("config-hooks")
	AgentCatalogCapability = modules.One[AgentCatalog]("agent-catalog")
)
