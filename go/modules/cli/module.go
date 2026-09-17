// Package cli 是人和组件图之间的命令行适配层。
//
// CLI 自己只拥有参数分派、输出和进程退出码。具体模块通过 many capability
// 贡献 Command 与 DiagnosticProvider，因此增加 OMO 等功能不需要修改一个
// 中央命令注册表。Host 则反向限定扩展命令可以调用的 CLI 能力。
//
// service 在 Start 时取得 AgentCatalog、Runtime 以及扩展列表，在 Stop 时
// 清空引用。main 最终只从 App 取出这一项 CLI capability 并调用 Run。
package cli

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"

	configapi "github.com/rzbdz/newgate/go/modules/config"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime"
)

type service struct {
	agents      confighookapi.AgentCatalog
	runtime     runtimeapi.Runtime
	commands    []Command
	diagnostics []DiagnosticProvider
}

var _ CLI = (*service)(nil)

// New 声明最终 CLI 入口，并在 Start 中一次性解析命令与诊断扩展。
// CLI 依赖端口快照而非全局注册表，因此运行期调用路径保持明确。
func New() modules.Component {
	service := &service{}
	return modules.Component{
		Name: "cli",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(runtimeapi.Capability),
			modules.Need(confighookapi.AgentCatalogCapability),
			modules.Optional(CommandsCapability),
			modules.Optional(DiagnosticsCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, CLI(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			service.agents = modules.MustGet(ctx, confighookapi.AgentCatalogCapability)
			service.runtime = modules.MustGet(ctx, runtimeapi.Capability)
			service.commands = modules.GetAll(ctx, CommandsCapability)
			service.diagnostics = modules.GetAll(ctx, DiagnosticsCapability)
			return nil
		},
		Stop: func(context.Context) error {
			service.agents = nil
			service.runtime = nil
			service.commands = nil
			service.diagnostics = nil
			return nil
		},
	}
}

// Run 注入本次构建信息后进入统一命令分派；模块命令仍通过 service 中的端口快照发现。
func (s *service) Run(args []string, build BuildInfo) int {
	Version = build.Version
	BuildTime = build.BuildTime
	CommitTime = build.CommitTime
	return runCLI(s, args)
}

func (s *service) moduleCommand(name string) (Command, bool) {
	for _, command := range s.commands {
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
	for _, provider := range s.diagnostics {
		out = append(out, provider.Diagnostics()...)
	}
	return out
}
