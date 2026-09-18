// Package gateway 把“选路并转发一次模型请求”实现为独立数据面。
//
// 请求先根据 profile 解析候选链，再依次尝试 binding。上游差异不能散落在这条
// 热路径里，所以 gateway 只公开 Plugin 注册端口：模型、客户端和交叉组件各自
// 注册只对自己成立的修补，网关统一负责排序、执行、记录 notes 和 fail-open。
//
// 组件层只管理插件注册表的所有权。HTTP server、重试、健康状态、协议拼接和
// 字节改写仍由本模块内部 package 负责；通用 component 内核不理解请求概念。
package gateway

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	configapi "github.com/rzbdz/newgate/go/modules/config"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"

	"github.com/rzbdz/newgate/go/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/go/modules/gateway/policy"
	"github.com/rzbdz/newgate/go/modules/gateway/quirk"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

type port struct {
	registry *special.Registry
	// filters 是数据面策略的账本。它**不装在 port 自己身上给外面看**——
	// Gateway 接口只暴露 RegisterFilter，读侧只有 forward.Server（Serve 期）
	// 和 handleStatus 用到。
	filters *policy.Registry
}

var _ Gateway = (*port)(nil)

// New 声明网关控制面组件。对 Config 的 Need 既表达真实依赖，
// 也确保所有配置语义先就绪，再允许插件进入请求路径。
//
// 它对 ui 只声明 **Optional**（弱依赖，见 component.Optional）：
// `newgate st` / `metrics` / `probe` 是这一层的用户界面，插件名与开关语义都是本
// 模块的知识，所以命令由本模块自己注册（见 command_special.go 的说明）。用
// Optional 而不是 Need 是必须的——网关不该因为「没装界面」起不来；而点击进界面
// 的那条边是**排序边**，保证本模块的 Start 跑的时候界面已经就绪。
func New() modules.Component {
	port := &port{registry: special.NewRegistry(), filters: policy.New()}
	var restore func()
	var restoreFilters func()
	var releases []modules.Release
	return modules.Component{
		Name: "gateway",
		Type: "gateway",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			// ui 是**弱依赖**（见 component.Optional）：`newgate st` / `metrics` /
			// `probe` 是本层的用户界面，装着界面就挂上去，没装就跳过——网关本身
			// 照常工作，只是没有入口。界面不依赖本模块（它没有任何出边），所以这
			// 条边不可能成环。
			modules.Optional(cliapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Gateway(port)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// state.json 里的 "gateway" 段归本模块（补丁开关、debug 到期时间）。
			// 登记它才有人拦「两个模块抢同一个字段」——以前这张表只覆盖了三处
			// 写入里的一半，gateway 与 runtime 的键被占用时不会当场报错。
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			fieldRelease, err := hooks.RegisterStateField("gateway", gatewaystate.Key)
			if err != nil {
				return err
			}
			releases = append(releases, fieldRelease)

			restore = special.InstallDefault(port.registry)

			// 数据面的策略账本也装成默认：起数据面的地方有两处（守护进程主循环
			// 走包内字段，testing/system 走这个入口），不装的话后者会自己 New()
			// 一本空账，把「贡献者真的接进数据面了吗」这类回归变成空转——那条
			// 形状判据的测试 2026-09-17 就是这么空转掉的。
			restoreFilters = policy.InstallDefault(port.filters)

			// ui 是**弱依赖**（见 component.Optional）：没装任何 ui 时这些命令就没有
			// 入口，但网关功能照常——本模块不依赖 ui 存在。
			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil
			}
			for _, cmd := range []cliapi.Command{
				specialCommand{}, schemaRepairCommand{}, debugCommand{},
				// 观测面也归数据面自己：计数器怎么分组、探活探出了什么，
				// 都是网关的语义（见 command_metrics.go / command_probe.go）。
				metricsCommand{}, probeCommand{}, logsCommand{},
				// 守护进程本体：`newgate __serve`。它以前是界面的命令，但它跑的
				// 是数据面（见 serve.go）。策略账本原样递进去——**这一层不认识
				// 任何一位策略**，谁插进来由各自的 Start 决定。
				serveCommand{filters: port.filters},
			} {
				release, err := cli.RegisterCommand(cmd)
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			// 补丁开关的状态行、代理那一行、以及两条体检（环境 / 代理）都归
			// 本模块自报（见 status.go / diagnostics.go）。
			for _, register := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterStatus(switchStatus{}) },
				func() (modules.Release, error) { return cli.RegisterStatus(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterDump(gatewayReporter{}) },
				func() (modules.Release, error) { return cli.RegisterVerbose(switchStatus{}) },
			} {
				release, err := register()
				if err != nil {
					return err
				}
				releases = append(releases, release)
			}
			return nil
		},
		Stop: func(context.Context) error {
			err := modules.ReleaseAll(releases)
			if restoreFilters != nil {
				restoreFilters()
			}
			if restore != nil {
				restore()
			}
			return err
		},
	}
}

// Quirks 见 api.go 的说明（数据面用的就是这一个实例）。
func (p *port) Quirks() *quirk.Table { return quirk.Default }

// RegisterRequestHook 把插件注册限制在 gateway owner 内部，并把撤销权交还调用组件。
func (p *port) RegisterRequestHook(hook Plugin) (modules.Release, error) {
	return p.registry.Register(hook)
}

// RegisterFilter 让策略把决策逻辑插进数据面的四个决策点。
//
// 这是**唯一的插入口**：数据面 handler、内部 registry、以及「谁插进来了」这件事
// 都不越过这个接口。所以 gateway 可以完全不认识那张健康表（binding 的可用性与
// 延迟账本），健康表只要声明 Need(gateway) 就能把自己的状态机挂上去。形状与
// RegisterRequestHook 并列，理由见 modules/gateway/policy 的包注释。
func (p *port) RegisterFilter(f Filter) (modules.Release, error) {
	return p.filters.Register(f)
}
