package builtin

import (
	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/contracts"
)

// manager is the process-wide default module set. Alternative loaders use
// component directly instead of mutating this singleton.
var manager = modules.Must(Loader{})

type Agent = agentapi.Agent
type Slot = agentapi.Slot

func catalog() contracts.AgentCatalog {
	return modules.MustGet(manager.Context(), contracts.AgentCatalogCapability)
}

func Get(id string) (*agentapi.Agent, bool) { return catalog().Get(id) }
func Names() []string                       { return catalog().Names() }

func CLI() contracts.CLI {
	return modules.MustGet(manager.Context(), contracts.CLICapability)
}
