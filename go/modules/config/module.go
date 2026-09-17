package config

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"

	"github.com/rzbdz/newgate/go/modules/config/roleprov"
)

type port struct{ roles *roleprov.Registry }

var _ Config = (*port)(nil)

// New 声明配置组件：它提供唯一 Config 端口，并在生命周期内安装动态档位注册表。
// Stop 恢复旧默认值，使测试和重复组装不会遗留进程级兼容状态。
func New() modules.Component {
	port := &port{roles: roleprov.NewRegistry()}
	var restore func()
	return modules.Component{
		Name: "config",
		Provides: []modules.Provision{
			modules.Provide(Capability, Config(port)),
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

// RegisterRoleProvider 把扩展所有权委托给 role registry，并原样返回 Release。
func (p *port) RegisterRoleProvider(provider RoleProvider) (modules.Release, error) {
	return p.roles.Register(provider)
}
