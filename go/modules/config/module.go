package config

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"

	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/roleprov"
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
			// ui 是**可选**的：没装任何 ui 时配置照常工作，只是没有命令行入口
			// （见 CLAUDE.md §4「ui 只是一类普通模块」）。
			modules.Inject(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Config(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = roleprov.InstallDefault(port.roles)

			// 配置**自己**的命令与展示面（tier / profiles / profile / --set-profile /
			// init / reload，体检的文件与链路两项，status 的「配置」一行与两张表）
			// 由本模块注入界面：界面不认识 profile，也不认识链。
			return nil
		},
		// 注入是**第二阶段**（见 modules.Inject）：ui 不参与排序，所以它可能
		// 比本模块晚起——Start 阶段它还没提供端口。Attach 在全图 Start 完之后跑。
		Attach: func(_ context.Context, ctx modules.Context) error {
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
