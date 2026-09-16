package deepseek

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	deepseekapi "github.com/rzbdz/newgate/go/modules/deepseek/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
)

func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name:     "deepseek",
		Requires: []modules.Requirement{modules.Need(gatewayapi.Capability)},
		Provides: []modules.Provision{
			modules.Provide(deepseekapi.Capability, deepseekapi.Model{
				MatchTarget: MatchTarget,
			}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			for _, treatment := range Treatments() {
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
