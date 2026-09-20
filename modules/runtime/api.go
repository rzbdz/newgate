package runtime

import (
	modules "github.com/rzbdz/newgate/component"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// Runtime 封装被接管客户端的启动边界，使 CLI 不需要知道 shim、环境注入和 argv 细节。
type Runtime interface {
	// Launch 注入 env 并 exec 真实客户端。facts 由调用方从目录端口取来（见
	// agentapi.AgentFacts）——「这个客户端此刻怎么样」是客户端的知识，本模块只转交。
	Launch(agent *agentapi.Agent, facts agentapi.AgentFacts, args []string, profile string) int
}

// Capability 标识唯一的客户端运行时实现。
var Capability = modules.NewCapability[Runtime]("runtime")
