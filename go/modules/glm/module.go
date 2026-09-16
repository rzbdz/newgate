package glm

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
)

type moduleProvider struct{}

var _ modules.Provider = (*moduleProvider)(nil)

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
	return modules.Component{
		Name: "glm",
		Provides: []modules.Provision{
			modules.Provide(contracts.GLMModel, contracts.ModelFamily{
				Name: "glm", MatchTarget: MatchTarget,
			}),
		},
	}
}
