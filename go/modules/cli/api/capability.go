package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
)

// Diagnostic 是模块交给 CLI 展示的一组结构化状态，
// 让模块保留诊断知识，而 CLI 只负责统一编排和输出。
type Diagnostic struct {
	Label   string
	State   string
	Line    string
	Details []string
}

// DiagnosticProvider 允许任意模块追加 doctor 信息，不需要 CLI 反向导入该模块。
type DiagnosticProvider interface {
	Diagnostics() []Diagnostic
}

// Host 是扩展命令可使用的最小 CLI 能力集合。
// 它避免 Command 获得整个 service，并明确哪些交互仍由主 CLI 统一控制。
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

// Command 是模块向 CLI 贡献的命令端口；Names 声明路由名，Run 执行命令语义。
type Command interface {
	Names() []string
	Run(Host, []string) int
}

// BuildInfo 把链接期版本信息显式传入 CLI，避免模块读取可变全局构建状态。
type BuildInfo struct {
	Version    string
	BuildTime  string
	CommitTime string
}

// CLI 是进程组合根最终调用的命令行入口。
type CLI interface {
	Run(args []string, build BuildInfo) int
}

// 这组三个 capability 把固定 CLI 入口与开放扩展点分开：
// CLI 只能有一个实现，命令和诊断则允许模块独立贡献。
var (
	Capability            = modules.One[CLI]("cli")
	CommandsCapability    = modules.Many[Command]("cli-commands")
	DiagnosticsCapability = modules.Many[DiagnosticProvider]("diagnostics")
)
