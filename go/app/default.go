package app

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"

	cliapi "github.com/rzbdz/newgate/go/modules/cli"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
	wrapperapi "github.com/rzbdz/newgate/go/modules/wrapper"
)

// App 是进程级组合根的所有权对象。
// 它持有 Manager 而不是复制服务，全进程的启动、查询和停止因而共享同一张图。
type App struct{ manager *modules.Manager }

// Agent 和 Slot 保留旧调用面的类型名，但事实定义仍由 confighook/api 拥有。
type Agent = agentapi.Agent

// Slot 是 confighook/api.Slot 的兼容别名，不在 builtin 重复定义槽位语义。
type Slot = agentapi.Slot

// New 构建并启动默认组件图；返回成功意味着所有必需端口已经解析且组件已启动。
func New(ctx context.Context) (*App, error) {
	manager, err := modules.NewContext(ctx, Loader{})
	if err != nil {
		return nil, err
	}
	return &App{manager: manager}, nil
}

// Context 暴露只读端口表，供真正的组合根获取最终入口，避免重新创建服务。
func (a *App) Context() modules.Context { return a.manager.Context() }

// ComponentNames 返回实际拓扑启动顺序，用于测试和运行时诊断。
func (a *App) ComponentNames() []string { return a.manager.ComponentNames() }

// Components 返回按启动顺序的组件定义（含 Type），供 newgate plugin 按分类枚举
// 全部模块。用图而不是让模块自报，是为了让「没参与开关体系的模块」也列得出来。
func (a *App) Components() []modules.Component { return a.manager.Components() }

// Stop 将整个应用生命周期交还 Manager 逆序收束。
func (a *App) Stop(ctx context.Context) error { return a.manager.Stop(ctx) }

func (a *App) catalog() agentapi.AgentCatalog {
	return modules.MustGet(a.manager.Context(), agentapi.AgentCatalogCapability)
}

// Get 从只读 AgentCatalog 查询客户端描述符。
func (a *App) Get(id string) (*agentapi.Agent, bool) { return a.catalog().Get(id) }

// Names 返回当前组件图注册的客户端名称。
func (a *App) Names() []string { return a.catalog().Names() }

// CLI 返回组件图中唯一的命令行入口。
func (a *App) CLI() cliapi.CLI {
	return modules.MustGet(a.manager.Context(), cliapi.Capability)
}

// Wrapper 返回 shim 接管入口。main 用它做 argv0 分发：被当成某个 client 调用
// 时走它，否则走 CLI。
func (a *App) Wrapper() wrapperapi.Wrapper {
	return modules.MustGet(a.manager.Context(), wrapperapi.Capability)
}
