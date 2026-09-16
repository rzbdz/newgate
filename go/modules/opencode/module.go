package opencode

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	opencodeapi "github.com/rzbdz/newgate/go/modules/opencode/api"
)

func New() modules.Component {
	var release modules.Release
	return modules.Component{
		Name:     "opencode",
		Requires: []modules.Requirement{modules.Need(confighookapi.ConfigHooksCapability)},
		Provides: []modules.Provision{
			modules.Provide(opencodeapi.Capability,
				opencodeapi.Client{AgentID: ID}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			var err error
			release, err = modules.MustGet(ctx, confighookapi.ConfigHooksCapability).
				RegisterAgent(Agent())
			return err
		},
		Stop: func(context.Context) error { return modules.ReleaseAll([]modules.Release{release}) },
	}
}
