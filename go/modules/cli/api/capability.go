package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
)

type Diagnostic struct {
	Label   string
	State   string
	Line    string
	Details []string
}

type DiagnosticProvider interface {
	Diagnostics() []Diagnostic
}

type Host interface {
	Die(code int, message string) int
	LiveRouting() (
		available func(provider, model string) bool,
		rank func(provider, model string) int,
	)
	PrintChain([]configapi.Step)
	PrintSkips([]configapi.Skip)
	NotifyProxy()
}

type Command interface {
	Names() []string
	Run(Host, []string) int
}

type BuildInfo struct {
	Version    string
	BuildTime  string
	CommitTime string
}

type CLI interface {
	Run(args []string, build BuildInfo) int
}

var (
	Capability            = modules.One[CLI]("cli")
	CommandsCapability    = modules.Many[Command]("cli-commands")
	DiagnosticsCapability = modules.Many[DiagnosticProvider]("diagnostics")
)
