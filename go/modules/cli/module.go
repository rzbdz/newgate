package cli

import (
	"context"
	"sync"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/contracts"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
)

type provider struct{}
type service struct{}

var (
	_ modules.Provider = (*provider)(nil)
	_ contracts.CLI    = (*service)(nil)

	extensionsMu sync.RWMutex
	commands     []contracts.CLICommand
	diagnostics  []contracts.DiagnosticProvider
)

func New() modules.Provider { return provider{} }

func (provider) Component() modules.Component {
	return modules.Component{
		Name: "cli",
		Requires: []modules.Requirement{
			modules.Need(contracts.ConfigCapability),
			modules.Need(contracts.RuntimeCapability),
			modules.Optional(contracts.CLICommandsCapability),
			modules.Optional(contracts.DiagnosticsCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(contracts.CLICapability, contracts.CLI(service{})),
		},
		Start: func(ctx modules.Context) error {
			extensionsMu.Lock()
			commands = modules.GetAll(ctx, contracts.CLICommandsCapability)
			diagnostics = modules.GetAll(ctx, contracts.DiagnosticsCapability)
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

func (service) Run(args []string, build contracts.BuildInfo) int {
	Version = build.Version
	BuildTime = build.BuildTime
	CommitTime = build.CommitTime
	return Run(args)
}

func componentCommand(name string) (contracts.CLICommand, bool) {
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

func componentDiagnostics() []contracts.Diagnostic {
	extensionsMu.RLock()
	defer extensionsMu.RUnlock()
	var out []contracts.Diagnostic
	for _, provider := range diagnostics {
		out = append(out, provider.Diagnostics()...)
	}
	return out
}

func agentCatalog() contracts.AgentCatalog { return agentstate.Catalog() }
