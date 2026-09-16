package cli

import (
	"context"
	"sync"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/api"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime/api"
)

type provider struct{}
type service struct{}

var (
	_ modules.Provider = (*provider)(nil)
	_ cliapi.CLI       = (*service)(nil)

	extensionsMu sync.RWMutex
	commands     []cliapi.Command
	diagnostics  []cliapi.DiagnosticProvider
)

func New() modules.Provider { return provider{} }

func (provider) Component() modules.Component {
	return modules.Component{
		Name: "cli",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(runtimeapi.Capability),
			modules.Optional(cliapi.CommandsCapability),
			modules.Optional(cliapi.DiagnosticsCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(cliapi.Capability, cliapi.CLI(service{})),
		},
		Start: func(ctx modules.Context) error {
			extensionsMu.Lock()
			commands = modules.GetAll(ctx, cliapi.CommandsCapability)
			diagnostics = modules.GetAll(ctx, cliapi.DiagnosticsCapability)
			extensionsMu.Unlock()
			return nil
		},
		Stop: func(context.Context) error {
			extensionsMu.Lock()
			commands = nil
			diagnostics = nil
			extensionsMu.Unlock()
			return nil
		},
	}
}

func (service) Run(args []string, build cliapi.BuildInfo) int {
	Version = build.Version
	BuildTime = build.BuildTime
	CommitTime = build.CommitTime
	return Run(args)
}

func moduleCommand(name string) (cliapi.Command, bool) {
	extensionsMu.RLock()
	defer extensionsMu.RUnlock()
	for _, command := range commands {
		for _, candidate := range command.Names() {
			if candidate == name {
				return command, true
			}
		}
	}
	return nil, false
}

func moduleDiagnostics() []cliapi.Diagnostic {
	extensionsMu.RLock()
	defer extensionsMu.RUnlock()
	var out []cliapi.Diagnostic
	for _, provider := range diagnostics {
		out = append(out, provider.Diagnostics()...)
	}
	return out
}

func agentCatalog() confighookapi.AgentCatalog { return agentstate.Catalog() }
