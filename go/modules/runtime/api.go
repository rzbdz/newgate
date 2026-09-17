package runtime

import (
	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
)

// Runtime 封装被接管客户端的启动边界，使 CLI 不需要知道 shim、环境注入和 argv 细节。
type Runtime interface {
	Launch(agent *agentapi.Agent, args []string, profile string) int
}

// Capability 标识唯一的客户端运行时实现。
var Capability = modules.NewCapability[Runtime]("runtime")
