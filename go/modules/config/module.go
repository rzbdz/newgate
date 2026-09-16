package config

import (
	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
)

type provider struct{}

var _ modules.Provider = (*provider)(nil)

func New() modules.Provider { return provider{} }

func (provider) Component() modules.Component {
	return modules.Component{
		Name: "config",
		Provides: []modules.Provision{
			modules.Provide(configapi.Capability, configapi.Config{}),
		},
	}
}
