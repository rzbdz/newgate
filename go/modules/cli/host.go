package cli

import (
	configapi "github.com/rzbdz/newgate/go/modules/config"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// moduleCLIHost exposes rendering and daemon notification primitives to
// module-owned commands without importing concrete modules into the CLI.
type moduleCLIHost struct{}

func (moduleCLIHost) Die(code int, message string) int { return die(code, message) }

func (moduleCLIHost) LiveRouting() (
	func(provider, model string) bool,
	func(provider, model string) int,
) {
	_, live := proxyState()
	return availableFromProxy(live), rankFromProxy(live)
}

// 链与跳过的**排版归 config**（本文件只转发）：那是配置的知识，界面借给模块
// 命令用一下而已。见 modules/config/commands.go 的 PrintChain / PrintSkips。
func (moduleCLIHost) PrintChain(steps []resolve.Step) { configapi.PrintChain(steps) }

func (moduleCLIHost) PrintSkips(skips []resolve.Skip) { configapi.PrintSkips(skips) }
func (moduleCLIHost) NotifyProxy()                    { notifyProxy() }
func (moduleCLIHost) PrintThinkCache()                { printThinkCache() }
func (moduleCLIHost) DaemonRunning() bool             { return daemon.Running() != nil }
