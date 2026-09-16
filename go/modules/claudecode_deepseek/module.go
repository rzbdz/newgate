package claudecode_deepseek

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	claudeapi "github.com/rzbdz/newgate/go/modules/claudecode/api"
	deepseekapi "github.com/rzbdz/newgate/go/modules/deepseek/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
)

func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "claudecode-deepseek",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(claudeapi.Capability),
			modules.Need(deepseekapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			client := modules.MustGet(ctx, claudeapi.Capability)
			model := modules.MustGet(ctx, deepseekapi.Capability)
			for _, treatment := range Treatments(client, model) {
				release, err := gateway.RegisterRequestHook(treatment)
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
