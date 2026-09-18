// Package cli 是人和组件图之间的命令行适配层。
//
// CLI 自己只拥有参数分派、输出和进程退出码。具体模块通过 RegisterCommand /
// RegisterDiagnostics 贡献命令与诊断，因此增加 OMO 等功能不需要修改一个中央
// 命令注册表。Host 则反向限定扩展命令可以调用的 CLI 能力。
//
// service 在 Start 时取得 AgentCatalog 与 Runtime，在 Stop 时清空引用；扩展
// 贡献由 Registry 账本管，各自的 Release 归贡献者自己保管。main 最终只从 App
// 取出这一项 CLI capability 并调用 Run。
package cli

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"

	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	configapi "github.com/rzbdz/newgate/go/modules/config"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime"
	surface "github.com/rzbdz/newgate/go/modules/surface"
)

// service 是**界面**：分派、渲染、进程生命周期。
//
// 它**不拥有**命令/诊断/状态行那三本账——那些归 modules/surface（一个叶子
// 模块），因为 cli 一旦同时是「账本所有者」和「界面」，依赖方向就成环：
// 想贡献命令的模块必须 Need(cli)，而 cli 又要 Need 它们才能渲染。环解开的方式
// 就是把账本下沉成叶子（见 modules/surface 的包注释）。
type service struct {
	agents  confighookapi.AgentCatalog
	runtime runtimeapi.Runtime
	health  breakerapi.Breaker

	ui surface.Surface
}

// BuildInfo 是链接期注入的版本信息；main 传进来，界面负责展示。
//
// 它留在 cli 而不是 surface：这是**界面自己的**身份，跟「模块往界面上贡献什么」
// 无关。surface 那边只有贡献契约。
type BuildInfo struct {
	Version    string
	BuildTime  string
	CommitTime string
}

// CLI 是进程组合根最终调用的命令行入口。
type CLI interface {
	Run(args []string, build BuildInfo) int
}

var _ CLI = (*service)(nil)

// New 声明最终 CLI 入口，并向其他模块开放命令与诊断两个扩展点。
//
// 依赖方向是 **cli → 扩展模块**（不是反过来）：cli 先起，扩展模块在 Start 里
// 拿到 cli 的 service 再注册。所以 opencodeomo 那条边由 registry 的所有者
// cli 决定，停止顺序自然反转成「扩展模块先停、cli 后停」——释放时目标还活着。
func New() modules.Component {
	service := &service{}
	return modules.Component{
		Name: "cli",
		Type: "cli",
		Requires: []modules.Requirement{
			// 账本在叶子模块里，cli 只是它的一个消费者。
			modules.Need(surface.Capability),
			modules.Need(configapi.Capability),
			modules.Need(runtimeapi.Capability),
			modules.Need(confighookapi.AgentCatalogCapability),
			// daemon 角色要用它构造数据面（forward.New 的第四个参数），
			// CLI 角色要用它把 /__newgate/status 的 breakers 解出来。
			modules.Need(breakerapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, CLI(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			service.ui = modules.MustGet(ctx, surface.Capability)
			service.agents = modules.MustGet(ctx, confighookapi.AgentCatalogCapability)
			service.runtime = modules.MustGet(ctx, runtimeapi.Capability)
			service.health = modules.MustGet(ctx, breakerapi.Capability)
			return nil
		},
		Stop: func(context.Context) error {
			service.agents = nil
			service.runtime = nil
			service.health = nil
			service.ui = nil
			return nil
		},
	}
}

// Run 注入本次构建信息后进入统一命令分派；模块命令从 Registry 账本里现取
// （不是启动时拍快照）——扩展模块可能比 CLI 晚一步才注册，现取才不会漏。
func (s *service) Run(args []string, build BuildInfo) int {
	Version = build.Version
	BuildTime = build.BuildTime
	CommitTime = build.CommitTime
	return runCLI(s, args)
}
