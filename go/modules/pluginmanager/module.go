package pluginmanager

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
)

// New 声明运行期开关账本。
//
// 形状就是一个普通模块：一个 capability、一个 Start、一个 Stop。**没有任何为它
// 加的内核钩子**——它出现在图里靠的是 tools/genmodules 扫目录，和别的 15 个模块
// 一样。它「特殊」只特殊在大部分模块选择依赖它。
//
// 依赖方向（2026-09-18 修正过一次）：
//
//	其他模块 ──Need──▶ plugin-manager ──Need──▶ cli ──Need──▶ confighook
//
// **cli 不认识本模块**，本模块认识 cli。上一版是反的（cli 为了渲染 `newgate plugin`
// 和 status 那一行去 Need 本模块），结果是本模块再也不能把自己的命令注册进 cli
// ——cli → pluginmanager → cliapi → cli 成环，于是命令只能被迫写在 cli 里。
// 现在状态行由本模块经 cli.RegisterStatus 自报、命令经 cli.RegisterCommand 自注册，
// cli 不需要认识任何模块，那个环就不存在了。
//
// 唯一的另一个 Requires 是 confighook：登记 state.json 字段必须走它的端口
// （与 modules/claudecode 登记 classifier_naked、classifier_override 同款）。
func New() modules.Component {
	service := &service{}
	var releases []modules.Release
	return modules.Component{
		Name: "plugin-manager",
		Type: TypeInfra,
		Requires: []modules.Requirement{
			modules.Need(cliapi.Capability),
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

			// 命令与状态行都是这个模块的用户界面，由它自己贡献——cli 不认识它。
			cli := modules.MustGet(ctx, cliapi.Capability)
			cmd := &command{manager: service}
			cmdRelease, err := cli.RegisterCommand(cmd)
			if err != nil {
				return err
			}
			releases = append(releases, cmdRelease)
			statusRelease, err := cli.RegisterStatus(cmd)
			if err != nil {
				return err
			}
			releases = append(releases, statusRelease)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
