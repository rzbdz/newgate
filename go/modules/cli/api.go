package cli

import (
	ext "github.com/rzbdz/newgate/go/modules/cli/extension"
)

// 本文件是 CLI 扩展契约的**类型别名转发层**。
//
// 契约本体住在 modules/cli/extension（叶子包），理由写在那个包的注释里：
// 契约若留在本包，任何想贡献命令的模块都得 import 这个重包（它拖着 runtime、
// gateway、breaker），于是 gateway 与 cli 成环、`newgate st` 这类命令永远回不了
// 自己的模块，runtime/gateway 的测试也无法再引用客户端模块。
//
// 这与 gateway/api.go 把插件契约下沉到 gateway/special、根包做别名转发是
// **同一条规矩**（docs/03-architecture.md §3）：契约类型若实现方需要反向引用，
// 定义下沉，根包转发。本包内的代码继续用这些短名字，不必到处写 ext.。
type (
	Diagnostic         = ext.Diagnostic
	DiagnosticProvider = ext.DiagnosticProvider
	StatusLine         = ext.StatusLine
	StatusProvider     = ext.StatusProvider
	Host               = ext.Host
	Command            = ext.Command
	HelpLine           = ext.HelpLine
	Documented         = ext.Documented
	BuildInfo          = ext.BuildInfo
	CLI                = ext.CLI
)

// Capability 是 CLI 的端口身份（定义连同 CLI 接口一起在 extension 包）。
var Capability = ext.Capability
