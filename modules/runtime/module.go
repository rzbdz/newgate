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

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	viewapi "github.com/rzbdz/newgate/lib/view"
	configapi "github.com/rzbdz/newgate/modules/config"
	confighookapi "github.com/rzbdz/newgate/modules/confighook"
	"github.com/rzbdz/newgate/modules/runtime/agentstate"

	"github.com/rzbdz/newgate/modules/runtime/launch"

	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
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
			// ui 是**弱依赖**（见 component.Optional）：接管的状态行与两条体检、
			// 以及本模块那一族命令（start/stop/on/off/restart…）都归它自己贡献，
			// 装着界面就挂上去，没装就跳过——接管照常工作，只是没有入口。
			modules.Optional(cliapi.Capability),
			// web 界面同理，而且是**另一条**：只装 dashboard 的装配里（没有终端
			// 界面），「谁在走 newgate」照样该出现在网页上。
			modules.Optional(viewapi.Capability),
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

			// web 界面那一份（谁在走 newgate）**先**注册：它不依赖 cli，只装
			// dashboard 的装配里也要有——下面那段一旦 return，这里就永远不会跑
			// （与 gateway/config 同一条）。
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := v.Register("runtime",
					viewapi.Title(func() string { return i18n.T("Takeover", nil) }).
						In(func() string { return i18n.T("Runtime", nil) }), takeoverConcepts)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			// 接管的状态行与两条体检（接管 / 备份）由本模块自报：写这些文件的
			// 是本模块，界面不该替它读 original/ 目录（见 diagnostics.go）。
			//
			// catalog 在这里现取（Start 的参数已经出了作用域，而下面两处都要用）。
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			all := append(commands(catalog), launchCommands(service, catalog)...)
			for _, cmd := range all {
				release, err := cli.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			reporter := runtimeReporter{agents: catalog}
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterStatus(reporter) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(reporter) },
				func() (modules.Release, error) { return cli.RegisterDump(reporter) },
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
func (*service) Launch(agent *confighookapi.Agent, facts confighookapi.AgentFacts, args []string, profile string) int {
	return launch.Launch(agent, facts, args, launch.Options{Profile: profile})
}
