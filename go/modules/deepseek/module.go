// Package deepseek 拥有 DeepSeek 模型家族的识别和上游协议修补。
//
// 模型家族根据 model/provider/base URL 判断请求是否属于自己，并注册只由
// DeepSeek 上游要求的 treatments。它不知道请求来自哪个客户端。
//
// 如果某个问题只在 Claude Code 与 DeepSeek 组合时出现，则交给
// claudecode_deepseek；这样模型模块不会把客户端猜测变成全局行为。
package deepseek

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	deepseekapi "github.com/rzbdz/newgate/go/modules/deepseek/api"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
)

// New 声明 DeepSeek 模型组件，同时提供模型判定端口并注册模型侧协议修补。
// 客户端与模型的组合行为不放在这里，而由独立交叉组件拥有。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name:     "deepseek",
		Requires: []modules.Requirement{modules.Need(gatewayapi.Capability)},
		Provides: []modules.Provision{
			modules.Provide(deepseekapi.Capability, deepseekapi.Model{
				MatchTarget: MatchTarget,
			}),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			gateway := modules.MustGet(ctx, gatewayapi.Capability)
			for _, treatment := range Treatments() {
				release, err := gateway.RegisterRequestHook(treatment)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
