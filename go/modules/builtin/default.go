package builtin

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/api"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
)

type App struct{ manager *modules.Manager }

type Agent = agentapi.Agent
type Slot = agentapi.Slot

func New(ctx context.Context) (*App, error) {
	manager, err := modules.NewContext(ctx, Loader{})
	if err != nil {
		return nil, err
	}
	return &App{manager: manager}, nil
}

func (a *App) Context() modules.Context { return a.manager.Context() }

func (a *App) ComponentNames() []string { return a.manager.ComponentNames() }

func (a *App) Stop(ctx context.Context) error { return a.manager.Stop(ctx) }

func (a *App) catalog() agentapi.AgentCatalog {
	return modules.MustGet(a.manager.Context(), agentapi.AgentCatalogCapability)
}

func (a *App) Get(id string) (*agentapi.Agent, bool) { return a.catalog().Get(id) }
func (a *App) Names() []string                       { return a.catalog().Names() }

func (a *App) CLI() cliapi.CLI {
	return modules.MustGet(a.manager.Context(), cliapi.Capability)
}
