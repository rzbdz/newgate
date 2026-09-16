package cli

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/api"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime/api"
)

type service struct {
	agents      confighookapi.AgentCatalog
	runtime     runtimeapi.Runtime
	commands    []cliapi.Command
	diagnostics []cliapi.DiagnosticProvider
}

var _ cliapi.CLI = (*service)(nil)

func New() modules.Component {
	service := &service{}
	return modules.Component{
		Name: "cli",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(runtimeapi.Capability),
			modules.Need(confighookapi.AgentCatalogCapability),
			modules.Optional(cliapi.CommandsCapability),
			modules.Optional(cliapi.DiagnosticsCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(cliapi.Capability, cliapi.CLI(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			service.agents = modules.MustGet(ctx, confighookapi.AgentCatalogCapability)
			service.runtime = modules.MustGet(ctx, runtimeapi.Capability)
			service.commands = modules.GetAll(ctx, cliapi.CommandsCapability)
			service.diagnostics = modules.GetAll(ctx, cliapi.DiagnosticsCapability)
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

func (s *service) Run(args []string, build cliapi.BuildInfo) int {
	Version = build.Version
	BuildTime = build.BuildTime
	CommitTime = build.CommitTime
	return runCLI(s, args)
}

func (s *service) moduleCommand(name string) (cliapi.Command, bool) {
	for _, command := range s.commands {
		for _, candidate := range command.Names() {
			if candidate == name {
				return command, true
			}
		}
	}
	return nil, false
}

func (s *service) moduleDiagnostics() []cliapi.Diagnostic {
	var out []cliapi.Diagnostic
	for _, provider := range s.diagnostics {
		out = append(out, provider.Diagnostics()...)
	}
	return out
}
