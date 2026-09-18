// Package extension 是模块向界面注入东西时实现的那组接口，以及界面自己的端口身份。
//
// # 为什么契约要下沉成叶子包
//
// 界面（modules/cli）是个重包：它 import runtime（daemon/takeover/injection）、
// gateway（forward/special/probe/…）和 breaker，因为它负责进程生命周期、数据面
// 装配和一堆渲染。模块若为了拿到 `Command` 接口去 import 它，就等于把这些全拖进
// 自己的依赖里，于是：
//
//   - 谁 import 了那个模块，就再也无法被 runtime / gateway 的**测试**引用
//     —— 那两处的测试要造客户端描述符，一 import 就成环（实测：
//     `runtime/launch` 与 `runtime/takeover` 的测试当场编译不过）；
//   - gateway 注册不了自己的命令（`newgate st` 只能被迫写在界面里）。
//
// 契约下沉之后，模块只 import 这个叶子包 + component + config，箭头是
// `modules/* → cli/extension → config`，环就没有了。这与 gateway/api.go 把插件
// 契约下沉到 gateway/special 是**同一条规矩**（docs/03-architecture.md §3）。
//
// # 账本在哪
//
// **账本（三本 Registry）在 modules/cli 自己的 service 上**，由它 Provide 出
// Capability。模块拿到的是一组注册回调，界面在分派命令、渲染 status / doctor 时
// 循环调用它们拿数据——界面**不 import 任何模块**，也不认识任何人。
//
// （2026-09-18 曾把账本挪进一个独立的 modules/surface 模块，那是多余的：它唯一
// 买到的是「界面可以去依赖模块」的自由，而界面的目标恰恰是**不依赖任何人**。
// 折回之后少一个模块、少一层概念，与「界面只提供注入点」的模型一致。）
//
// # 边界
//
// 本包只允许 import component 与 config（都是轻的基础设施）。它一旦变重，
// 上面那条「模块引得起」立刻失效。
package extension

import (
	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config"
	"github.com/rzbdz/newgate/go/modules/config/domain"
)

// Diagnostic 是模块交给 CLI 展示的一组结构化状态，
// 让模块保留诊断知识，而 CLI 只负责统一编排和输出。
type Diagnostic struct {
	Label   string
	State   string
	Line    string
	Details []string
}

// DiagnosticProvider 允许任意模块追加 doctor 信息，不需要 CLI 反向导入该模块。
type DiagnosticProvider interface {
	Diagnostics() []Diagnostic
}

// StatusLine 是模块贡献给 `newgate status` 的一行（字段名 + 值）。
//
// 与 Diagnostic 分开是因为两者的**时机**不同：doctor 是人主动敲的体检，可以
// 展开细节；status 是每次都会看一眼的概览，只该占一行。
type StatusLine struct {
	Label string
	Value string
}

// StatusProvider 允许模块给 `newgate status` 贡献一行，理由同 DiagnosticProvider：
// 模块保留自己的知识，CLI 只负责排版。
//
// 为什么需要它（而不是让 CLI 直接去读某个模块）：`status` 要显示「哪些开关点是
// 非出厂态」，而那份账本归 plugin-manager。CLI 若为了这一行去读它，那一行就会
// 把两个模块焊在一起——「谁的状态谁自己报」是解开这种耦合的唯一办法。
type StatusProvider interface {
	Status(*domain.State) []StatusLine
}

// Host 是扩展命令可使用的最小 CLI 能力集合。
// 它避免 Command 获得整个 CLI 内部，并明确哪些交互仍由主 CLI 统一控制。
//
// 加一个方法要慎重：它是**所有**模块命令都能看到的面积。判断标准是「这件事
// 只有 CLI 做得成」——而不是「这样我就不用把逻辑搬过去了」。
type Host interface {
	Die(code int, message string) int
	LiveRouting() (
		available func(provider, model string) bool,
		rank func(provider, model string) int,
	)
	PrintChain([]configapi.Step)
	PrintSkips([]configapi.Skip)
	NotifyProxy()

	// DaemonRunning 守护进程现在在跑吗（读 pidfile）。
	//
	// 只有 CLI 做得成这件事：它拥有进程生命周期（起停、优雅交接、pidfile
	// 的读写时机）。命令想问的其实是「我这一改有人立刻读吗」——不在跑就只是
	// 落了个盘，下次起来才生效，跟用户说清楚比让他以为已经生效强。
	DaemonRunning() bool

	// PrintThinkCache 打印推理缓存的命中计数。
	//
	// 数字只能从**跑着的守护进程**取：缓存在守护进程的内存里，CLI 是另一个
	// 进程，在命令这边读只会看到一个空缓存——那比不显示更误导人。所以取数
	// 这件事归 CLI（它持有那个 HTTP 客户端），命令只管在合适的位置调一下。
	PrintThinkCache()
}

// Command 是模块向 CLI 贡献的命令端口；Names 声明路由名，Run 执行命令语义。
//
// **args 里没有命令名**：`newgate plugin deepseek off` 分派到 `plugin` 这条命令时，
// Run 收到的是 `["deepseek", "off"]`——动词已被分派器剥掉。这条是 2026-09-18 用
// 真实二进制跑出来的：契约没写它，于是照 `os.Args` 的直觉按 1 起下标写，整条命令
// 错位一格（`plugin <模块>` 打成了列表、`plugin <路径> off 90s` 报「不认识的动词
// 90s」）。下标基准这种事必须写在契约上，不能靠猜。
//
// Names 里的多个名字是**同一个命令**的别名，分派到哪一个都收到同样形状的 args。
type Command interface {
	Names() []string
	Run(Host, []string) int
}

// Arg 取模块命令的第 i 个参数，越界给空串。**下标从 0 起**——args 里没有命令名，
// 见 Command 的契约说明。
//
// 为什么把它放在契约包里而不是让每个模块自己写一个三行的取参函数：2026-09-18
// 两个模块各自写了一遍，两遍都把下标写成从 1 起，两遍都错位一格。下标基准这种东西
// 每重写一次就多一次猜错的机会，所以只留一份实现。
func Arg(args []string, i int) string {
	if i < 0 || i >= len(args) {
		return ""
	}
	return args[i]
}

// HelpLine 是命令在 `newgate --help` 里占的那一行。
type HelpLine struct {
	Section string // 归到哪一节：接管 / 跑一次 / 路由与配置 / 探测与观测 / 维护 / 模块
	Usage   string // 左列，命令写法
	Summary string // 右列，一句话结论
}

// Documented 是 Command 的可选搭档：命令自己声明它在 --help 里长什么样。
//
// 为什么必须有它：命令从 CLI 的 switch 搬回各模块之后，`newgate --help` 不能再
// 硬编码那些行——**硬编码就等于「命令搬了、CLI 还认识它」，白搬**。help 是
// 「CLI 认识哪些模块」的另一个面，两个面得一起搬。
//
// 没实现它的命令仍然可用，只是不在 help 里占一行。那不是一个好状态（用户看不见
// 的命令约等于不存在），所以新命令都该实现它——但它是可选接口，因为它是**呈现**
// 关注点，不该挡住一条命令先能跑起来。
type Documented interface {
	Help() HelpLine
}

// CLI 既是进程组合根最终调用的入口，也是模块注入自己那一份东西的端口。
//
// 两件事共用一个接口是有意的：它们都是「界面这件事」的两面——外面把一次命令行
// 调用交给它（Run），模块把自己的一部分挂到它上面（RegisterXxx）。分成两个端口
// 只会让每个模块都要 Need 两次、而它们永远是同一个组件提供的。
//
// 为什么注入必须是 Register 而不是让模块 Provide 一个「命令端口」：后者没有生命
// 周期。模块认领一个命令名之后没人能撤销它，也没人查重——两个模块认领同一个名字
// 是静默先到先得（2026-09-17 实测）。走 Register 则每次注入都拿到一个 Release，
// 由注入方自己在 Stop 时释放。
//
// **读侧不在这里**：命令账本、诊断、状态行都归界面自己，它直接读自己的 service，
// 不需要经过接口。
type CLI interface {
	Run(args []string, build BuildInfo) int

	// RegisterCommand 注入一条命令。命令名（Names）是查重的逻辑键，撞名当场
	// 报错而不是先到先得。
	RegisterCommand(Command) (modules.Release, error)
	// RegisterDiagnostics 注入一组 doctor 输出。诊断可叠加，没有键命名空间，
	// 因此不查重，只记 token。
	RegisterDiagnostics(DiagnosticProvider) (modules.Release, error)
	// RegisterStatus 注入 `newgate status` 里的若干行，理由同 RegisterDiagnostics。
	RegisterStatus(StatusProvider) (modules.Release, error)
}

// BuildInfo 把链接期版本信息显式传入界面，避免模块读取可变全局构建状态。
type BuildInfo struct {
	Version    string
	BuildTime  string
	CommitTime string
}

// Capability 是界面的端口身份。模块用它注入自己的命令与状态行；进程组合根用它
// 把这次调用交给界面。
var Capability = modules.NewCapability[CLI]("cli")
