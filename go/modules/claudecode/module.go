package claudecode

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name: "claudecode",
		Requires: []modules.Requirement{
			modules.Need(contracts.GatewayCapability),
			modules.Need(contracts.ConfigHooksCapability),
			modules.Need(contracts.ThinkingCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(contracts.ClaudeCodeClient,
				contracts.ClientFamily{Name: "claudecode", AgentID: ID}),
		},
		Start: func(ctx modules.Context) error {
			config := modules.MustGet(ctx, contracts.ConfigHooksCapability)
			if err := config.RegisterAgent(Agent()); err != nil {
				return err
			}
			if err := config.RegisterStateField("claudecode", "classifier_override"); err != nil {
				return err
			}
			gateway := modules.MustGet(ctx, contracts.GatewayCapability)
			thinking := modules.MustGet(ctx, contracts.ThinkingCapability)
			for _, treatment := range Treatments(thinking) {
				gateway.RegisterRequestHook(treatment)
			}
			return nil
		},
	}
}
