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
			modules.Optional(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Gateway(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = special.InstallDefault(port.registry)

			// ui 是**可选**的（见 CLAUDE.md §4「ui 只是一类普通模块」）：没装任何
			// ui 时这些命令就没有入口，但网关功能照常——本模块不依赖 ui 存在。
			if cli, ok := modules.Get(ctx, cliapi.Capability); ok {
				for _, cmd := range []cliapi.Command{specialCommand{}, schemaRepairCommand{}, debugCommand{}} {
					release, err := cli.RegisterCommand(cmd)
					if err != nil {
						return err
					}
					releases = append(releases, release)
				}
				// 补丁开关的状态行也归本模块自报（见 status.go）。
				statusRelease, err := cli.RegisterStatus(switchStatus{})
				if err != nil {
					return err
				}
				releases = append(releases, statusRelease)
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
