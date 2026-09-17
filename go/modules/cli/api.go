package cli

import (
	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config"
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

// CLI 是进程组合根最终调用的命令行入口，也是命令与诊断的**扩展点所有者**。
//
// 为什么扩展点长在 CLI 自己的 service 上，而不是另开一个「命令」端口让模块
// 往里面 Provide：后者没有生命周期。模块认领一个命令名之后没人能撤销它，也
// 没人查重——两个模块认领同一个名字是静默先到先得（2026-09-17 实测）。走
// Register 则每次贡献都拿到一个 Release，owner 负责在 Stop 时逆序释放。
// 全仓库的跨模块贡献从此只有这一种写法，见 docs/09-extension-guide.md §3。
type CLI interface {
	Run(args []string, build BuildInfo) int

	// RegisterCommand 贡献一条命令。命令名（Names）是查重的逻辑键，撞名当场
	// 报错而不是先到先得。
	RegisterCommand(Command) (modules.Release, error)
	// RegisterDiagnostics 贡献一组 doctor 输出。诊断是可叠加的，没有键命名
	// 空间，因此不查重，只记 token。
	RegisterDiagnostics(DiagnosticProvider) (modules.Release, error)
}

// Capability 是 CLI 的端口身份。
var Capability = modules.NewCapability[CLI]("cli")
