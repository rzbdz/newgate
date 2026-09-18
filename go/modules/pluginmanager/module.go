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
// 依赖方向（2026-09-18 修正两次，这里是**当前**的形状）：
//
//	其他模块 ──Need──▶ plugin-manager ──Need──▶ confighook
//	其他模块 ──Inject──▶ cli（注入命令与状态行）
//
// 两处都要看清楚：
//
//   - **本模块不依赖 cli**，只有一条 Inject 边（见 component.Inject）。上一版是
//     `Need(cli)`：cli 为了渲染 `newgate plugin` 与那一行 status 去 Need 本模块，
//     本模块又要 Need(cli) 才能注册命令 —— 环，于是命令只能被迫写在 cli 里。
//   - **cli 也不依赖本模块**（它的 Requires 是空的）。状态行经 cli.RegisterStatus
//     自报、命令经 cli.RegisterCommand 自注册，方向是「本模块 → ui」，界面不认识
//     任何人。Inject 不参与排序，所以这两条箭头不会互相顶。
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
			modules.Inject(cliapi.Capability),
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

			// 命令与状态行都是这个模块的用户界面，由它自己贡献——界面不认识它。
			// ui 可选（见 CLAUDE.md §4）：没装任何 ui 就没有入口，开关账本照常工作。
			return nil
		},
		// 注入是**第二阶段**（见 modules.Inject）：ui 不参与排序，所以它可能
		// 比本模块晚起——Start 阶段它还没提供端口。Attach 在全图 Start 完之后跑。
		Attach: func(_ context.Context, ctx modules.Context) error {
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
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
