package claudecode_deepseek

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name: "claudecode-deepseek",
		Requires: []modules.Requirement{
			modules.Need(contracts.GatewayCapability),
			modules.Need(contracts.ClaudeCodeClient),
			modules.Need(contracts.DeepSeekModel),
		},
		Start: func(ctx modules.Context) error {
			gateway := modules.MustGet(ctx, contracts.GatewayCapability)
			client := modules.MustGet(ctx, contracts.ClaudeCodeClient)
			model := modules.MustGet(ctx, contracts.DeepSeekModel)
			for _, treatment := range Treatments(client, model) {
				gateway.RegisterRequestHook(treatment)
			}
			return nil
		},
	}
}
