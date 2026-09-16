package claudecode

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	claudeapi "github.com/rzbdz/newgate/go/modules/claudecode/api"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
	thinkingapi "github.com/rzbdz/newgate/go/modules/thinking/api"
)

func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
			modules.Need(thinkingapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(claudeapi.Capability,
				claudeapi.Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			config := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := config.RegisterAgent(Agent())
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = config.RegisterStateField("claudecode", "classifier_override")
			if err != nil {
				return err
			}
			releases = append(releases, release)
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			thinking := modules.MustGet(ctx, thinkingapi.Capability)
			for _, treatment := range Treatments(thinking) {
				release, err = gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
