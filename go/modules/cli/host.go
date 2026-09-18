package cli

import (
	"fmt"

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

func (moduleCLIHost) PrintChain(steps []resolve.Step) {
	fmt.Print(numberedBindingChain(steps, nil))
}

func (moduleCLIHost) PrintSkips(skips []resolve.Skip) { printSkips(skips) }
func (moduleCLIHost) NotifyProxy()                    { notifyProxy() }
func (moduleCLIHost) PrintThinkCache()                { printThinkCache() }
func (moduleCLIHost) DaemonRunning() bool             { return daemon.Running() != nil }
