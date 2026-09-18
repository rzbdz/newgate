// Package tui 是 menuconfig 风格的终端界面：改 profile / 档位归属用的。
//
// # 它是一个**独立的 ui 模块**（2026-09-18 从 modules/cli 的里层子包抽出来）
//
// 界面不是只有 cli 一个。tui 与将来的 web 都是同级的东西：各自是一个模块，各自
// 往**当前装着的** ui 里注入自己的入口（见 CLAUDE.md §4「ui 只是一类普通模块」）。
//
// 抽出来的直接好处是 modules/cli 里少了一块和「命令行解析」无关的东西——它原来
// 住在 cli/tui/ 里，只因为它是从 cli 敲出来的，而不是因为它属于命令行。
//
// # 它依赖什么
//
// 只依赖 config/store（读写 profile）。**不依赖 cli**：它只是把一个命令注入进去，
// 而那是可选的（没装任何 ui 时这个模块什么也不做，自己的功能不受影响）。
package tui

import (
	"context"
	"fmt"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
)

// New 声明终端界面模块。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "tui",
		// Type 是产品层的分类词，取值由编排者约定（见 modules/pluginmanager）。
		Type: "cli",
		Requires: []modules.Requirement{
			// ui 是**可选**的：没装 ui 时它没有入口，但模块本身照常成立
			// （它没有别的职责）。这正是「业务模块不依赖 ui」那条规矩。
			modules.Optional(cliapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			release, err := ui.RegisterCommand(tuiCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// tuiCommand 是 `newgate tui`（别名 menuconfig）。
type tuiCommand struct{}

var (
	_ cliapi.Command    = tuiCommand{}
	_ cliapi.Documented = tuiCommand{}
)

func (tuiCommand) Names() []string { return []string{"tui", "menuconfig"} }

func (tuiCommand) Help() cliapi.HelpLine {
	// Rank 50 = 维护那一节（与界面自己的那批同节）。数字是约定，留了空档给插队。
	return cliapi.HelpLine{
		Section: "维护",
		Rank:    50,
		Usage:   "tui",
		Summary: "menuconfig 风格界面",
	}
}

func (tuiCommand) Run(host cliapi.Host, _ []string) int {
	if err := Run(); err != nil {
		return host.Die(70, fmt.Sprintf("tui: %v", err))
	}
	return 0
}
