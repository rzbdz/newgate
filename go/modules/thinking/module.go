package thinking

import (
	modules "github.com/rzbdz/newgate/go/component"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
	thinkingapi "github.com/rzbdz/newgate/go/modules/thinking/api"
)

type moduleProvider struct{}
type service struct{}

var _ modules.Provider = (*moduleProvider)(nil)
var _ thinkingapi.Service = (*service)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name:     "thinking",
		Requires: []modules.Requirement{modules.Need(gatewayapi.Capability)},
		Provides: []modules.Provision{
			modules.Provide(thinkingapi.Capability, thinkingapi.Service(service{})),
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

func (service) BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, request)
}
