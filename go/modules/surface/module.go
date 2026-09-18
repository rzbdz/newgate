package surface

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
)

// New 声明「进程对用户暴露的那一层」的注册表组件。
//
// # 它为什么没有任何 Requires
//
// 这是它存在的全部理由：**它是叶子**，谁都能依赖它。所有想贡献命令的模块
// （gateway / runtime / config / claudecode / plugin-manager / …）都 Need 它，
// 而它谁也不 Need——所以那些模块彼此之间不必互相认识，CLI 也不必被它们认识。
//
// 上一版这三本账长在 modules/cli 上，于是「想贡献命令」就必须 Need(cli)，
// 而 cli 自己又要 Need(runtime/config/gateway) 才能渲染与启动客户端——两条边
// 首尾相接成环，`newgate start` / `tier` / `probe` 这些命令因此永远搬不回
// 自己的模块。拆出这个叶子之后环就断了。
//
// # 它不做的事
//
// 不渲染、不读盘、不认识任何具体命令。它只有三本账。分派、排版、进程生命周期
// 全是 modules/cli 的事——**cli 只是界面**。
func New() modules.Component {
	return modules.Component{
		Name: "surface",
		Type: "infra",
		Provides: []modules.Provision{
			modules.Provide(Capability, Surface(&service{})),
		},
		// 账本本身没有生命周期（Registry 零值可用，贡献的撤销权在贡献者手里，
		// 每次 Register 返回的 Release 由他们自己在 Stop 里释放）。所以这里
		// 两个函数都是空的——不是偷懒，是没有可做的事。
		Start: func(context.Context, modules.Context) error { return nil },
		Stop:  func(context.Context) error { return nil },
	}
}
