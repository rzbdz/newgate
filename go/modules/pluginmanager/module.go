package pluginmanager

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
)

// New 声明运行期开关账本。
//
// 形状就是一个普通模块：一个 capability、一个 Start、一个 Stop。**没有任何为它
// 加的内核钩子**——它出现在图里靠的是 tools/genmodules 扫目录，和别的 15 个模块
// 一样。它「特殊」只特殊在大部分模块选择依赖它。
//
// 唯一的 Requires 是 confighook：登记 state.json 字段必须走它的端口（与
// modules/claudecode 登记 classifier_naked、classifier_override 同款）。它不依赖
// 任何业务模块，所以排得很靠前，但这不是特权，只是没有业务依赖。
func New() modules.Component {
	service := &service{}
	var releases []modules.Release
	return modules.Component{
		Name: "plugin-manager",
		Type: TypeInfra,
		Requires: []modules.Requirement{
			modules.Need(confighookapi.ConfigHooksCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Manager(service)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			release, err := hooks.RegisterStateField("plugin-manager", StateKey)
			if err != nil {
				return err
			}
			releases = append(releases, release)

			// 它自己也在册，走的是同一个 RegisterSelf、同一个前缀规则——不因为
			// 自己是账本就有后门。newgate plugin 会把它列在 infra 组里。
			self, err := service.RegisterSelf("plugin-manager", nil)
			if err != nil {
				return err
			}
			releases = append(releases, self)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
