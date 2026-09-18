package breaker

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
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
	return modules.Component{
		Name: "breaker",
		Type: "infra",
		Provides: []modules.Provision{
			modules.Provide(Capability, Breaker(table)),
		},
		Start: func(context.Context, modules.Context) error {
			_ = table.UseFile(paths.HealthFile())
			return nil
		},
	}
}
