package cli

import (
	modules "github.com/rzbdz/newgate/go/component"
	surface "github.com/rzbdz/newgate/go/modules/surface"
)

// 本文件是**贡献契约的类型别名转发层**。
//
// 契约本体住在 modules/surface（叶子模块），理由写在那个包的注释里：账本一旦长在
// cli 自己身上，依赖方向就成环——想贡献命令的模块必须 Need(cli)，而 cli 又要
// Need 它们才能渲染，于是 `newgate start` / `tier` / `probe` 这些命令永远搬不回
// 自己的模块。
//
// 本包内的代码继续用这些短名字，不必到处写 surface.。
//
// 注意这里**没有** CLI / BuildInfo / Capability：那三个是**界面自己的**身份
// （进程组合根调用 cli.Run、main 注入版本信息），不是「模块往界面上贡献什么」的
// 契约。它们定义在 module.go。
type (
	Diagnostic         = surface.Diagnostic
	DiagnosticProvider = surface.DiagnosticProvider
	StatusLine         = surface.StatusLine
	StatusProvider     = surface.StatusProvider
	Host               = surface.Host
	Command            = surface.Command
	HelpLine           = surface.HelpLine
	Documented         = surface.Documented
)

// Capability 是 CLI（界面本身）的端口身份，供进程组合根取用。
//
// 与 surface.Capability 的分工：那个是「往界面上贡献东西」的口，所有模块都用它；
// 这个是「把这次命令行调用交给界面」的口，只有 main 用。
var Capability = modules.NewCapability[CLI]("cli")
