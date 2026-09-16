package glm

import (
	modules "github.com/rzbdz/newgate/go/component"
	glmapi "github.com/rzbdz/newgate/go/modules/glm/api"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name: "glm",
		Provides: []modules.Provision{
			modules.Provide(glmapi.Capability, glmapi.Model{
				Name: "glm", MatchTarget: MatchTarget,
			}),
		},
	}
}
