// Package runtime 把一个 Agent 描述符变成真实客户端进程。
//
// confighook 决定“客户端是什么”，config 决定“当前走哪个 profile”，runtime
// 只负责找到被 shim 遮住的真实二进制、构造最小环境覆盖并启动它。它不解析
// 模型链，也不修改客户端配置。
//
// 现有 launch 代码仍通过 agentstate 兼容桥读取目录。Runtime 组件在 Start
// 安装这份只读目录，在 Stop 恢复旧值，把遗留全局状态限制在明确生命周期内。
package runtime

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"

	"github.com/rzbdz/newgate/go/modules/runtime/launch"

	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
)

type service struct{}

var _ Runtime = (*service)(nil)

// New 声明客户端运行时组件。启动时把只读 AgentCatalog 接入兼容层，
// 停止时撤销该桥接；Launch 端口本身保持无状态。
func New() modules.Component {
	var restore func()
	var releases []modules.Release
	service := &service{}
	return modules.Component{
		Name: "runtime",
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(confighookapi.AgentCatalogCapability),
			// ui 是可选的：没装 ui 时接管照常工作，只是没有 `newgate status`
			// 里那一行和 doctor 的那两项（见 CLAUDE.md §4）。
			modules.Inject(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Runtime(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			catalog := modules.MustGet(ctx, confighookapi.AgentCatalogCapability)
			restore = agentstate.Set(catalog)

			// state.json 里的 taken_over 归本模块（on/off 收尾时写它）。登记它
			// 才有人拦「两个模块抢同一个字段」——这张表以前只覆盖三处写入里的一半。
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			fieldRelease, err := hooks.RegisterStateField("runtime", "taken_over")
			if err != nil {
				return err
			}
			releases = append(releases, fieldRelease)

			// 接管的状态行与两条体检（接管 / 备份）由本模块自报：写这些文件的
			// 是本模块，界面不该替它读 original/ 目录（见 diagnostics.go）。
			return nil
		},
		// 注入是**第二阶段**（见 modules.Inject）：ui 不参与排序，所以它可能
		// 比本模块晚起——Start 阶段它还没提供端口。Attach 在全图 Start 完之后跑。
		Attach: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			// catalog 在 Attach 里现取（Start 里那份是局部变量，注入阶段已经出了作用域）。
			agents := modules.MustGet(ctx, confighookapi.AgentCatalogCapability)
			all := append(commands(agents), launchCommands(service, agents)...)
			for _, cmd := range all {
				release, err := ui.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			reporter := runtimeReporter{agents: agents}
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return ui.RegisterStatus(reporter) },
				func() (modules.Release, error) { return ui.RegisterDiagnostics(reporter) },
				func() (modules.Release, error) { return ui.RegisterDump(reporter) },
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

// Launch 把端口调用翻译为 launch.Options，隔离 runtime 内部启动协议。
func (*service) Launch(agent *confighookapi.Agent, args []string, profile string) int {
	return launch.Launch(agent, args, launch.Options{Profile: profile})
}
