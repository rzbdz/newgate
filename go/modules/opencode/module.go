package opencode

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
)

type moduleProvider struct {
}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name:     "opencode",
		Requires: []modules.Requirement{modules.Need(contracts.ConfigHooksCapability)},
		Provides: []modules.Provision{
			modules.Provide(contracts.OpenCodeClient,
				contracts.ClientFamily{Name: "opencode"}),
		},
		Start: func(ctx modules.Context) error {
			return modules.MustGet(ctx, contracts.ConfigHooksCapability).RegisterAgent(Agent())
		},
	}
}
