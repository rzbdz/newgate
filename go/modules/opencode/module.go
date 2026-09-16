package opencode

import (
	modules "github.com/rzbdz/newgate/go/component"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	opencodeapi "github.com/rzbdz/newgate/go/modules/opencode/api"
)

type moduleProvider struct {
}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name:     "opencode",
		Requires: []modules.Requirement{modules.Need(confighookapi.ConfigHooksCapability)},
		Provides: []modules.Provision{
			modules.Provide(opencodeapi.Capability,
				opencodeapi.Client{Name: "opencode"}),
		},
		Start: func(ctx modules.Context) error {
			return modules.MustGet(ctx, confighookapi.ConfigHooksCapability).RegisterAgent(Agent())
		},
	}
}
