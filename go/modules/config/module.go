package config

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
	"github.com/rzbdz/newgate/go/modules/config/roleprov"
)

type port struct{ roles *roleprov.Registry }

var _ configapi.Config = (*port)(nil)

func New() modules.Component {
	port := &port{roles: roleprov.NewRegistry()}
	var restore func()
	return modules.Component{
		Name: "config",
		Provides: []modules.Provision{
			modules.Provide(configapi.Capability, configapi.Config(port)),
		},
		Start: func(context.Context, modules.Context) error {
			restore = roleprov.InstallDefault(port.roles)
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

func (p *port) RegisterRoleProvider(provider configapi.RoleProvider) (modules.Release, error) {
	return p.roles.Register(provider)
}
