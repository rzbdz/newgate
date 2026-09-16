package deepseek

import (
	modules "github.com/rzbdz/newgate/go/component"
	deepseekapi "github.com/rzbdz/newgate/go/modules/deepseek/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name:     "deepseek",
		Requires: []modules.Requirement{modules.Need(gatewayapi.Capability)},
		Provides: []modules.Provision{
			modules.Provide(deepseekapi.Capability, deepseekapi.Model{
				Name: "deepseek", MatchTarget: MatchTarget,
			}),
		},
		Start: func(ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			for _, treatment := range Treatments() {
				gateway.RegisterRequestHook(treatment)
			}
			return nil
		},
	}
}
