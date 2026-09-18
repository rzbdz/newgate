// Package surface 是「进程对用户暴露的那一层」的注册表：命令、诊断、状态行。
//
// # 为什么它必须是一个独立的模块（2026-09-18）
//
// 这些注册表原来长在 modules/cli 自己的 service 上。那让 cli 同时是两样东西：
// **注册表的所有者**和**交互界面**（分派 + 渲染 + 进程生命周期）。于是依赖方向
// 被锁死：
//
//	gateway / runtime / config ──Need──▶ cli        （为了注册自己的命令）
//	cli ──Need──▶ runtime / config / breaker        （为了渲染与启动客户端）
//
// 两条边首尾相接就是**环**。后果不是「不优雅」，是具体的能力搬不动：
// `newgate start`（runtime 的）、`newgate tier`（config 的）、`newgate probe`
// （gateway 的）——**每一个想搬回自己模块的命令都搬不动**，因为那个模块一旦
// Need(cli) 就与 cli 现有的 Need 成环。所以它们只能继续写在 cli 里，而 cli
// 就继续认识它本不该认识的每一个模块。
//
// 把注册表下沉成这个叶子模块之后，箭头变成：
//
//	所有模块 ──Need──▶ surface ◀──Need── cli
//
// surface 没有任何业务依赖，所以谁都能依赖它；cli 回到「只是界面」——它依赖
// surface 去分派与渲染，而**别人依赖 surface 而不是 cli**，环就没有了。
//
// # 边界（这条是硬要求）
//
// 本包只允许 import component 与 config/domain（都是轻的基础设施）。它一旦
// 变重——尤其是一旦 import 了任何业务模块——上面那条「谁都能依赖它」立刻失效，
// 环会原样回来。所以别把渲染、日志、HTTP 这些东西放进来：那些是 cli 的事。
//
// # 谁提供什么
//
// 本模块只拥有「贡献点」与账本，不拥有任何具体命令。命令由拥有那项能力的
// 模块自己注册（`newgate st` 归 gateway、`newgate naked` 归 claudecode、
// `newgate plugin` 归 plugin-manager）。cli 只负责分派与排版。
package surface

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

// Surface 是这个模块对外提供的端口：模块往这里贡献，CLI 从这里取来分派与排版。
//
// 为什么贡献必须是 Register 而不是让模块 Provide 一个「命令端口」：后者没有生命
// 周期。模块认领一个命令名之后没人能撤销它，也没人查重——两个模块认领同一个名字
// 是静默先到先得（2026-09-17 实测）。走 Register 则每次贡献都拿到一个 Release，
// 由贡献者自己在 Stop 时释放。
type Surface interface {
	// RegisterCommand 贡献一条命令。命令名（Names）是查重的逻辑键，撞名当场
	// 报错而不是先到先得。
	RegisterCommand(Command) (modules.Release, error)
	// RegisterDiagnostics 贡献一组 doctor 输出。诊断是可叠加的，没有键命名
	// 空间，因此不查重，只记 token。
	RegisterDiagnostics(DiagnosticProvider) (modules.Release, error)
	// RegisterStatus 贡献 `newgate status` 里的若干行，理由同 RegisterDiagnostics。
	RegisterStatus(StatusProvider) (modules.Release, error)

	// Commands 当前全部命令（按注册顺序），供分派与 --help 组装。
	Commands() []Command
	// Lookup 按名字找一条命令。Names 里的每个别名都能命中。
	Lookup(name string) (Command, bool)
	// Diagnostics 收集全部模块的 doctor 输出。
	Diagnostics() []Diagnostic
	// Statuses 收集全部模块贡献的 status 行。
	Statuses(*domain.State) []StatusLine
}

// Capability 是这一层的端口身份。模块用它注册自己的命令；CLI 用它取来分派。
var Capability = modules.NewCapability[Surface]("surface")
