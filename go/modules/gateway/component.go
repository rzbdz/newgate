// Package gateway exposes the proxy's extension surface as the root runtime
// component. It adapts the existing data plane without teaching the component
// manager what a request hook is.
package gateway

import (
	"context"
	"fmt"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

type port struct{ registry *special.Registry }
type provider struct {
	port    *port
	restore func()
}

var (
	_ contracts.Gateway = (*port)(nil)
	_ modules.Provider  = (*provider)(nil)
)

func New() modules.Provider {
	return provider{port: &port{registry: special.NewRegistry()}}
}

func (p provider) Component() modules.Component {
	instance := &p
	return modules.Component{
		Name: "gateway",
		Provides: []modules.Provision{
			modules.Provide(contracts.GatewayCapability, contracts.Gateway(p.port)),
		},
		Start: func(modules.Context) error {
			instance.restore = special.InstallDefault(instance.port.registry)
			return nil
		},
		Stop: func(context.Context) error {
			if instance.restore != nil {
				instance.restore()
			}
			return nil
		},
	}
}

func (p *port) RegisterRequestHook(hook special.Plugin) { p.registry.Register(hook) }

func (*port) AgentBaseURL(port int, agentID string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/a/%s", port, agentID)
}
