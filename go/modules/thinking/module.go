package thinking

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

type moduleProvider struct{}
type service struct{}

var _ modules.Provider = (*moduleProvider)(nil)
var _ contracts.ThinkingService = (*service)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name:     "thinking",
		Requires: []modules.Requirement{modules.Need(contracts.GatewayCapability)},
		Provides: []modules.Provision{
			modules.Provide(contracts.ThinkingCapability, contracts.ThinkingService(service{})),
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

func (service) BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, request)
}
