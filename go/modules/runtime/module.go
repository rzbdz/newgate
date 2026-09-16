package runtime

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime/api"
	"github.com/rzbdz/newgate/go/modules/runtime/launch"
)

type service struct{}

var _ runtimeapi.Runtime = (*service)(nil)

func New() modules.Component {
	var restore func()
	service := &service{}
	return modules.Component{
		Name: "runtime",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(confighookapi.AgentCatalogCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(runtimeapi.Capability, runtimeapi.Runtime(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = agentstate.Set(modules.MustGet(ctx, confighookapi.AgentCatalogCapability))
			return nil
		},
		Stop: func(context.Context) error {
			if restore != nil {
				restore()
			}
			return nil
		},
	}
}

func (*service) Launch(agent *confighookapi.Agent, args []string, profile string) int {
	return launch.Launch(agent, args, launch.Options{Profile: profile})
}
