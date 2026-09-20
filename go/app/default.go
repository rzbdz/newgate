package app

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
)

// App 是进程级组合根的所有权对象。
//
// # 它认识谁
//
// **一个模块都不认识。** 这是它的定义，不是风格：组合根要与「摘掉一个模块」这个
// 操作可交换——用户把任意一个非 built-in 模块从清单里去掉，本包必须照样编译、
// 照样装配、照样能跑（少掉的只是那个模块提供的东西）。只要这里出现一行
// `modules/<某个业务模块>` 的 import，那条可交换性就没了，而且症状很隐蔽：
// **编译期**才炸，而摘模块的人多半是在配置层动手的。
//
// 判据是机械的，写在 app/direction_test.go：
//
//	command grep -rn '"github.com/rzbdz/newgate/go/modules' app/*.go
//	→ 只许出现 modules_gen.go 那一行（构建期生成的装配清单）
//
// # 它怎么把这次调用交出去
//
// 它不认识 cli，也不认识 wrapper：两者（以及将来的 web-daemon）都在自己的 Start
// 里往 **root**（唯一的 built-in，见 go/root）的入口账本申报，这里只问账本一次。
// 「谁是入口」因此成了模块自己的知识，换界面在组合根上是零改动。
type App struct{ manager *modules.Manager }

// New 构建并启动组件图；返回成功意味着所有必需端口已经解析且组件已启动。
//
// 不传 loader = 内核自带的那张图（Loader{}）。传 nil 也一样——调用方普遍写成
// 「opts.Loader 为 nil 就用默认」，让这里兜住比让每个调用方各兜一遍好。
//
// 传自己的 loader 是**消费者组自己的图**的正门（见 Selection）：组合根不认识
// 任何模块，它只认识「谁交给我哪些组件」。
func New(ctx context.Context, loaders ...modules.Loader) (*App, error) {
	live := make([]modules.Loader, 0, len(loaders))
	for _, l := range loaders {
		if l != nil {
			live = append(live, l)
		}
	}
	if len(live) == 0 {
		live = append(live, Loader{})
	}
	manager, err := modules.NewContext(ctx, live...)
	if err != nil {
		return nil, err
	}
	return &App{manager: manager}, nil
}

// Context 暴露只读端口表，供真正的组合根获取最终入口，避免重新创建服务。
func (a *App) Context() modules.Context { return a.manager.Context() }

// ComponentNames 返回实际拓扑启动顺序，用于测试和运行时诊断。
func (a *App) ComponentNames() []string { return a.manager.ComponentNames() }

// Components 返回按启动顺序的组件定义（含 Type）。
func (a *App) Components() []modules.Component { return a.manager.Components() }

// Stop 将整个应用生命周期交还 Manager 逆序收束。
func (a *App) Stop(ctx context.Context) error { return a.manager.Stop(ctx) }
