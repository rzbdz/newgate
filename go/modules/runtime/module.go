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
)

type service struct{}

var _ Runtime = (*service)(nil)

// New 声明客户端运行时组件。启动时把只读 AgentCatalog 接入兼容层，
// 停止时撤销该桥接；Launch 端口本身保持无状态。
func New() modules.Component {
	var restore func()
	service := &service{}
	return modules.Component{
		Name: "runtime",
		Type: "client",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(confighookapi.AgentCatalogCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Runtime(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			restore = agentstate.Set(modules.MustGet(ctx, confighookapi.AgentCatalogCapability))
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

// Launch 把端口调用翻译为 launch.Options，隔离 runtime 内部启动协议。
func (*service) Launch(agent *confighookapi.Agent, args []string, profile string) int {
	return launch.Launch(agent, args, launch.Options{Profile: profile})
}
