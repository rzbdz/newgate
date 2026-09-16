package cli

import (
	"fmt"

	"github.com/rzbdz/newgate/go/modules/config/resolve"
)

// componentCLIHost exposes rendering and daemon notification primitives to
// component-owned commands without importing concrete components into the CLI.
type componentCLIHost struct{}

func (componentCLIHost) Die(code int, message string) int { return die(code, message) }

func (componentCLIHost) LiveRouting() (
	func(provider, model string) bool,
	func(provider, model string) int,
) {
	_, live := proxyState()
	return availableFromProxy(live), rankFromProxy(live)
}

func (componentCLIHost) PrintChain(steps []resolve.Step) {
	fmt.Print(numberedBindingChain(steps, nil))
}

func (componentCLIHost) PrintSkips(skips []resolve.Skip) { printSkips(skips) }
func (componentCLIHost) NotifyProxy()                    { notifyProxy() }
