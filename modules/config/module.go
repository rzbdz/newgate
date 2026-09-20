package config

import (
	"context"

	modules "github.com/rzbdz/newgate/component"

	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/roleprov"
)

type port struct{ roles *roleprov.Registry }

var _ Config = (*port)(nil)

// New 声明配置组件：它提供唯一 Config 端口，并在生命周期内安装动态档位注册表。
// Stop 恢复旧默认值，使测试和重复组装不会遗留进程级兼容状态。
func New() modules.Component {
	port := &port{roles: roleprov.NewRegistry()}
	var restore func()
	var releases []modules.Release
	return modules.Component{
		Name: "config",
		Type: "infra",
		Requires: []modules.Requirement{
			// ui 是**弱依赖**（见 component.Optional）：装着界面就把本模块自己的
			// 命令与展示面挂上去；没装就跳过，配置照常工作，只是没有命令行入口。
			modules.Optional(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Config(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = roleprov.InstallDefault(port.roles)

			// 配置**自己**的命令与展示面（tier / profiles / profile / --set-profile /
			// init / reload，体检的文件与链路两项，status 的「配置」一行与两张表）
			// 由本模块注入界面：界面不认识 profile，也不认识链。
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			for _, cmd := range commands() {
				release, err := ui.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return ui.RegisterDiagnostics(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterStatus(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterStatusBlocks(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterDump(reporter{}) },
				func() (modules.Release, error) { return ui.RegisterGlossary(glossary{}) },
			} {
				release, err := register()
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			if restore != nil {
				restore()
			}
			return err
		},
	}
}

// RegisterRoleProvider 把扩展所有权委托给 role registry，并原样返回 Release。
func (p *port) RegisterRoleProvider(provider RoleProvider) (modules.Release, error) {
	return p.roles.Register(provider)
}
