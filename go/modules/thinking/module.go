package thinking

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
	thinkingapi "github.com/rzbdz/newgate/go/modules/thinking/api"
)

type service struct{}

var _ thinkingapi.Service = (*service)(nil)

func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name:     "thinking",
		Requires: []modules.Requirement{modules.Need(gatewayapi.Capability)},
		Provides: []modules.Provision{
			modules.Provide(thinkingapi.Capability, thinkingapi.Service(service{})),
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

func (service) BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, request)
}
