package api

import (
	modules "github.com/rzbdz/newgate/go/component"
)

type ConfigHooks interface {
	RegisterAgent(*Agent) (modules.Release, error)
	BindTakeover(agentID string, takeover ConfigTakeover) (modules.Release, error)
	RegisterStateField(owner, name string) (modules.Release, error)
}

type AgentCatalog interface {
	Get(id string) (*Agent, bool)
	Names() []string
}

var (
	ConfigHooksCapability  = modules.One[ConfigHooks]("config-hooks")
	AgentCatalogCapability = modules.One[AgentCatalog]("agent-catalog")
)
