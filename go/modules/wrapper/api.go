package wrapper

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
)

// Capability 标识进程内唯一的 shim 接管实现。
var Capability = modules.NewCapability[Wrapper]("wrapper")

// Wrapper 是「PATH shim」这条接管通路的完整所有权：一端是把链接装出去
// （Install/Uninstall），另一端是链接被敲响时的那次进程调用（Dispatch）。
//
// 为什么两端必须同属一个模块：它们是同一个机制的两半，分开就会出现
// 「装了链接却没人解析 argv0」的静默断链。链接本身是个符号链接
// ~/.config/newgate/bin/claude → newgate 二进制；内核 exec 它时 argv[0] 是
// `claude`，Dispatch 正是靠这一点认出「这次调用是替 claude 跑的」。
//
// 底层字节操作（建符号链接、改 shell rc）仍是 runtime/injection 这个库；
// 本模块拥有的是**策略**——哪些名字算 client、什么条件才认领一次调用。
type Wrapper interface {
	// Dispatch 处理 argv0 分发路径。当 argv[0] 的 basename 是某个已注册
	// client 时，注入 env 并 exec 它；返回 (退出码, true)。
	// 不是 client 调用则返回 (0, false)，调用方继续走控制 CLI。
	//
	// 成功路径不会返回：进程被 syscall.Exec 替换掉了。返回值只在失败或
	// 「不是 client 调用」时出现。
	Dispatch(ctx context.Context, argv []string) (int, bool)

	// Install 为某个 client 建 shim 链接，返回链接路径。
	Install(agentID string) (string, error)

	// Uninstall 摘掉一个 shim。名字必须是已知 client，且链接必须是我们装的
	// ——同名的真实可执行文件、用户自己的包装脚本一律不动。
	Uninstall(agentID string) error

	// Installed 列出我们自己装的 shim 名。
	Installed() []string

	// Foreign 列出 shim 目录里不是我们装的条目。只展示，不删。
	Foreign() []string
}
