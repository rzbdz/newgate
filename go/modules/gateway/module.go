// Package gateway exposes the proxy's extension surface as the root runtime
// component. It adapts the existing data plane without teaching the component
// manager what a request hook is.
package gateway

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

type port struct{ registry *special.Registry }

var _ gatewayapi.Gateway = (*port)(nil)

func New() modules.Component {
	port := &port{registry: special.NewRegistry()}
	var restore func()
	return modules.Component{
		Name:     "gateway",
		Requires: []modules.Requirement{modules.Need(configapi.Capability)},
		Provides: []modules.Provision{
			modules.Provide(gatewayapi.Capability, gatewayapi.Gateway(port)),
		},
		Start: func(context.Context, modules.Context) error {
			restore = special.InstallDefault(port.registry)
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

func (p *port) RegisterRequestHook(hook gatewayapi.Plugin) (modules.Release, error) {
	return p.registry.Register(hook)
}
