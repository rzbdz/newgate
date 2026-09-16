package builtin

import (
	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/api"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
)

// manager is the process-wide default module set. Alternative loaders use
// component directly instead of mutating this singleton.
var manager = modules.Must(Loader{})

type Agent = agentapi.Agent
type Slot = agentapi.Slot

func catalog() agentapi.AgentCatalog {
	return modules.MustGet(manager.Context(), agentapi.AgentCatalogCapability)
}

func Get(id string) (*agentapi.Agent, bool) { return catalog().Get(id) }
func Names() []string                       { return catalog().Names() }

func CLI() cliapi.CLI {
	return modules.MustGet(manager.Context(), cliapi.Capability)
}
