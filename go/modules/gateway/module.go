// Package gateway 把“选路并转发一次模型请求”实现为独立数据面。
//
// 请求先根据 profile 解析候选链，再依次尝试 binding。上游差异不能散落在这条
// 热路径里，所以 gateway 只公开 Plugin 注册端口：模型、客户端和交叉组件各自
// 注册只对自己成立的修补，网关统一负责排序、执行、记录 notes 和 fail-open。
//
// 组件层只管理插件注册表的所有权。HTTP server、重试、健康状态、协议拼接和
// 字节改写仍由本模块内部 package 负责；通用 component 内核不理解请求概念。
package gateway

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	configapi "github.com/rzbdz/newgate/go/modules/config"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

type port struct{ registry *special.Registry }

var _ Gateway = (*port)(nil)

// New 声明网关控制面组件。对 Config 的 Need 既表达真实依赖，
// 也确保所有配置语义先就绪，再允许插件进入请求路径。
//
// 它也 Need(cli)：`newgate st` 是这一层的用户界面，插件名与开关语义都是本模块的
// 知识，所以命令由本模块自己注册（见 command_special.go 的说明）。这条边不会成环
// ——cli 靠 modules/surface 那个叶子模块得到契约，不 import 任何业务模块。
func New() modules.Component {
	port := &port{registry: special.NewRegistry()}
	var restore func()
	var releases []modules.Release
	return modules.Component{
		Name: "gateway",
		Type: "gateway",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			// 数据面要把健康表交给转发服务（forward.New 的第四个参数），
			// 守护进程主循环也归本模块（见 serve.go）。
			modules.Need(breakerapi.Capability),
			modules.Inject(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Gateway(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = special.InstallDefault(port.registry)

			// ui 是**可选**的（见 CLAUDE.md §4「ui 只是一类普通模块」）：没装任何
			// ui 时这些命令就没有入口，但网关功能照常——本模块不依赖 ui 存在。
			return nil
		},
		// 注入是**第二阶段**（见 modules.Inject）：ui 不参与排序，所以它可能
		// 比本模块晚起——Start 阶段它还没提供端口。Attach 在全图 Start 完之后跑。
		Attach: func(_ context.Context, ctx modules.Context) error {
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			for _, cmd := range []cliapi.Command{
				specialCommand{}, schemaRepairCommand{}, debugCommand{},
				// 观测面也归数据面自己：计数器怎么分组、探活探出了什么，
				// 都是网关的语义（见 command_metrics.go / command_probe.go）。
				metricsCommand{}, probeCommand{}, logsCommand{},
				// 守护进程本体：`newgate __serve`。它以前是界面的命令，但它跑的
				// 是数据面（见 serve.go）。
				serveCommand{health: modules.MustGet(ctx, breakerapi.Capability)},
			} {
				release, err := cli.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			// 补丁开关的状态行、代理那一行、以及两条体检（环境 / 代理）都归
			// 本模块自报（见 status.go / diagnostics.go）。
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterStatus(switchStatus{}) },
				func() (modules.Release, error) { return cli.RegisterStatus(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(gatewayReporter{}) },
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

// RegisterRequestHook 把插件注册限制在 gateway owner 内部，并把撤销权交还调用组件。
func (p *port) RegisterRequestHook(hook Plugin) (modules.Release, error) {
	return p.registry.Register(hook)
}
