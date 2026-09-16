// Package contracts contains capability interfaces shared by otherwise
// independent components. Interfaces live with consumers, not implementations.
package contracts

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/confighook/roleprov"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

type Gateway interface {
	RegisterRequestHook(special.Plugin)
	AgentBaseURL(port int, agentID string) string
}

type ConfigHooks interface {
	RegisterAgent(*agentapi.Agent) error
	BindTakeover(agentID string, takeover agentapi.ConfigTakeover) error
	RegisterRoleProvider(roleprov.Provider)
	RegisterStateField(owner, name string) error
}

type AgentCatalog interface {
	Get(id string) (*agentapi.Agent, bool)
	Names() []string
	StateFieldOwner(name string) (string, bool)
}

type Diagnostic struct {
	Label   string
	State   string
	Line    string
	Details []string
}

type DiagnosticProvider interface {
	Diagnostics() []Diagnostic
}

type CLIHost interface {
	Die(code int, message string) int
	LiveRouting() (
		available func(provider, model string) bool,
		rank func(provider, model string) int,
	)
	PrintChain([]resolve.Step)
	PrintSkips([]resolve.Skip)
	NotifyProxy()
}

type CLICommand interface {
	Names() []string
	Run(CLIHost, []string) int
}

type BuildInfo struct {
	Version    string
	BuildTime  string
	CommitTime string
}

type CLI interface {
	Run(args []string, build BuildInfo) int
}

type Runtime struct{}

type ClientFamily struct {
	Name    string
	AgentID string
}

type ModelFamily struct {
	Name        string
	MatchTarget func(model, provider, baseURL string) bool
}

type ThinkingService interface {
	BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error)
}

var (
	GatewayCapability      = modules.One[Gateway]("gateway")
	ConfigHooksCapability  = modules.One[ConfigHooks]("config-hooks")
	AgentCatalogCapability = modules.One[AgentCatalog]("agent-catalog")

	ClaudeCodeClient      = modules.One[ClientFamily]("client-family.claudecode")
	OpenCodeClient        = modules.One[ClientFamily]("client-family.opencode")
	DeepSeekModel         = modules.One[ModelFamily]("model-family.deepseek")
	GLMModel              = modules.One[ModelFamily]("model-family.glm")
	ThinkingCapability    = modules.One[ThinkingService]("thinking")
	DiagnosticsCapability = modules.Many[DiagnosticProvider]("diagnostics")
	CLICommandsCapability = modules.Many[CLICommand]("cli-commands")
	RuntimeCapability     = modules.One[Runtime]("runtime")
	CLICapability         = modules.One[CLI]("cli")
)
