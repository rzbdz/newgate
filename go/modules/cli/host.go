package cli

import (
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"
)

// moduleCLIHost exposes rendering and daemon notification primitives to
// module-owned commands without importing concrete modules into the CLI.
type moduleCLIHost struct{}

func (moduleCLIHost) Die(code int, message string) int { return die(code, message) }

func (moduleCLIHost) NotifyProxy() { notifyProxy() }

// DaemonRunning 守护进程现在在跑吗。
//
// 走控制面叶子的 State()（它读 pidfile），而不是直接 import runtime/daemon：
// 界面**不认识 runtime**，而「怎么知道进程在不在」是数据面与控制面之间的事。
func (moduleCLIHost) DaemonRunning() bool {
	info, _ := controlplane.State()
	return info != nil
}
