package breaker

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/paths"
)

// New 声明健康表组件。
//
// 它没有任何 Requires：健康表只认 binding 键、状态码和延迟，不认识 agent、
// profile 或上游协议。它是被注入的那一方——gateway 的数据面在建链期问它
// 「这条 binding 能不能用」，CLI 把 Snapshot 渲染成 `newgate breaker`。
//
// 装载 health.json 失败**不**让装配失败：坏掉的健康表只该退化成纯内存，不能
// 因为一个可再生的缓存文件让整个代理起不来。错误存下来，等 SetErrorHandler
// 装上后补报出去（「不静默」）。
func New() modules.Component {
	table := newTable()
	var releases []modules.Release
	return modules.Component{
		Name: "breaker",
		Type: "infra",
		Requires: []modules.Requirement{
			// 只有**注入**（见 component.Inject）：本模块不认识界面，界面也不
			// 依赖本模块——`newgate breaker` 是这张健康表的用户界面，归本模块。
			modules.Inject(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Breaker(table)),
		},
		Start: func(context.Context, modules.Context) error {
			_ = table.UseFile(paths.HealthFile())
			return nil
		},
		// 注入是第二阶段：ui 不参与排序，Start 时它可能还没提供端口。
		Attach: func(_ context.Context, ctx modules.Context) error {
			ui, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			release, err := ui.RegisterCommand(breakerCommand{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
