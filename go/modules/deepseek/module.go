package deepseek

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name:     "deepseek",
		Requires: []modules.Requirement{modules.Need(contracts.GatewayCapability)},
		Provides: []modules.Provision{
			modules.Provide(contracts.DeepSeekModel, contracts.ModelFamily{
				Name: "deepseek", MatchTarget: MatchTarget,
			}),
		},
		Start: func(ctx modules.Context) error {
			gateway := modules.MustGet(ctx, contracts.GatewayCapability)
			for _, treatment := range Treatments() {
				gateway.RegisterRequestHook(treatment)
			}
			return nil
		},
	}
}
