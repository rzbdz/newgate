package runtime

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
)

type provider struct{}

var _ modules.Provider = (*provider)(nil)

func New() modules.Provider { return provider{} }

func (provider) Component() modules.Component {
	var restore func()
	return modules.Component{
		Name: "runtime",
		Requires: []modules.Requirement{
			modules.Need(contracts.ConfigCapability),
			modules.Need(contracts.AgentCatalogCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(contracts.RuntimeCapability, contracts.Runtime{}),
		},
		Start: func(ctx modules.Context) error {
			restore = agentstate.Set(modules.MustGet(ctx, contracts.AgentCatalogCapability))
			return nil
		},
		Stop: func(context.Context) error {
			if restore != nil {
				restore()
			}
			return nil
		},
	}
}
