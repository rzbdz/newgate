package claudecode

import (
	modules "github.com/rzbdz/newgate/go/component"
	claudeapi "github.com/rzbdz/newgate/go/modules/claudecode/api"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
	thinkingapi "github.com/rzbdz/newgate/go/modules/thinking/api"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name: "claudecode",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
			modules.Need(thinkingapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(claudeapi.Capability,
				claudeapi.Client{Name: "claudecode", AgentID: ID}),
		},
		Start: func(ctx modules.Context) error {
			config := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			if err := config.RegisterAgent(Agent()); err != nil {
				return err
			}
			if err := config.RegisterStateField("claudecode", "classifier_override"); err != nil {
				return err
			}
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			thinking := modules.MustGet(ctx, thinkingapi.Capability)
			for _, treatment := range Treatments(thinking) {
				gateway.RegisterRequestHook(treatment)
			}
			return nil
		},
	}
}
