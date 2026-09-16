package claudecode_glm

import (
	modules "github.com/rzbdz/newgate/go/component"
	claudeapi "github.com/rzbdz/newgate/go/modules/claudecode/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
	glmapi "github.com/rzbdz/newgate/go/modules/glm/api"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name: "claudecode-glm",
		Requires: []modules.Requirement{
			modules.Need(gatewayapi.Capability),
			modules.Need(claudeapi.Capability),
			modules.Need(glmapi.Capability),
		},
		Start: func(ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			client := modules.MustGet(ctx, claudeapi.Capability)
			model := modules.MustGet(ctx, glmapi.Capability)
			for _, treatment := range Treatments(client, model) {
				gateway.RegisterRequestHook(treatment)
			}
			return nil
		},
	}
}
