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
	"fmt"

	modules "github.com/rzbdz/newgate/go/component"

	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	configapi "github.com/rzbdz/newgate/go/modules/config"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime"
)

type service struct {
	agents  confighookapi.AgentCatalog
	runtime runtimeapi.Runtime
	health  breakerapi.Breaker

	commands    modules.Registry[Command]
	diagnostics modules.Registry[DiagnosticProvider]
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
			service.agents = modules.MustGet(ctx, confighookapi.AgentCatalogCapability)
			service.runtime = modules.MustGet(ctx, runtimeapi.Capability)
			service.health = modules.MustGet(ctx, breakerapi.Capability)
			return nil
		},
		Stop: func(context.Context) error {
			service.agents = nil
			service.runtime = nil
			service.health = nil
			return nil
		},
	}
}

// RegisterCommand 贡献一条命令；命令名撞车当场报错。
//
// 报错文案是中文：这条是**面向插件作者**的业务冲突（「我的命令名被谁占了」），
// 不是框架级装配错误，跟 cli 里其他用户可见的文案保持一致。查重在
// Registry.Register 的写锁内跑，所以并发注册同一个名字也只会有一个成功。
func (s *service) RegisterCommand(command Command) (modules.Release, error) {
	if command == nil {
		return nil, fmt.Errorf("cli: 命令不能为 nil")
	}
	names := command.Names()
	if len(names) == 0 {
		return nil, fmt.Errorf("cli: 命令必须至少声明一个名字（Names）")
	}
	return s.commands.Register(command, func(existing []Command) error {
		for _, other := range existing {
			for _, have := range other.Names() {
				for _, want := range names {
					if have == want {
						return fmt.Errorf("cli: 命令名 %q 已被占用", want)
					}
				}
			}
		}
		return nil
	})
}

// RegisterDiagnostics 贡献一组 doctor 输出。诊断可叠加，不查重。
func (s *service) RegisterDiagnostics(provider DiagnosticProvider) (modules.Release, error) {
	if provider == nil {
		return nil, fmt.Errorf("cli: 诊断提供者不能为 nil")
	}
	return s.diagnostics.Register(provider, nil)
}

// Run 注入本次构建信息后进入统一命令分派；模块命令从 Registry 账本里现取
// （不是启动时拍快照）——扩展模块可能比 CLI 晚一步才注册，现取才不会漏。
func (s *service) Run(args []string, build BuildInfo) int {
	Version = build.Version
	BuildTime = build.BuildTime
	CommitTime = build.CommitTime
	return runCLI(s, args)
}

func (s *service) moduleCommand(name string) (Command, bool) {
	for _, command := range s.commands.All() {
		for _, candidate := range command.Names() {
			if candidate == name {
				return command, true
			}
		}
	}
	return nil, false
}

func (s *service) moduleDiagnostics() []Diagnostic {
	var out []Diagnostic
	for _, provider := range s.diagnostics.All() {
		out = append(out, provider.Diagnostics()...)
	}
	return out
}
