package glm

import (
	modules "github.com/rzbdz/newgate/go/component"
	glmapi "github.com/rzbdz/newgate/go/modules/glm/api"
)

func New() modules.Component {
	return modules.Component{
		Name: "glm",
		Provides: []modules.Provision{
			modules.Provide(glmapi.Capability, glmapi.Model{
				MatchTarget: MatchTarget,
			}),
		},
	}
}
