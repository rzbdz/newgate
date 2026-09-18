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
// 本包只允许 import component 与 config 的**叶子**（domain / resolve，都是轻的
// 基础设施）。它一旦变重，上面那条「模块引得起」立刻失效。
//
// 2026-09-18 起这里 import 的是 config/resolve 而不是 config 根包：根包要
// import 本包（模块往界面注入东西时要用这里的类型），两者互引就成环了。链的
// Step / Skip 本来就定义在 resolve（config/api.go 只是别名转发），所以直接引
// 叶子即可——这正是 docs/03-architecture.md §3 那条「定义下沉到实现包」。
package extension

import (
	"strings"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
)

// Diagnostic 是模块交给 CLI 展示的一组结构化状态，
// 让模块保留诊断知识，而 CLI 只负责统一编排和输出。
type Diagnostic struct {
	// Rank 决定行序（小的在前）。与 StatusLine.Rank 同理：体检项的先后是版面
	// 判断，由贡献者声明，界面不写死任何一项。
	Rank    int
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
	// Rank 决定**行序**（小的在前）。同 Rank 保持注册顺序，稳定。
	//
	// 「代理排第一行」这类判断是**版面**判断，所以由贡献者声明而不是界面写死
	// ——界面一旦写死「哪一行排第一」，那一行就成了界面认识某个模块的证据。
	Rank  int
	Label string
	Value string
}

// StatusBlock 是一段**已经排好版**的状态输出：可选节标题 + 若干行。
//
// 为什么允许预渲染（而不是只报 label/value）：`status` 里有两块是表格（档位绑定、
// fallback 链），它们的排版与数据是一体的 know-how——让界面重排就得把 resolve
// 的知识搬回界面，那正是这一轮要拆掉的东西。所以块的**内容与排版**都由 owner 给，
// 界面只决定**位置**（Rank）。
//
// 它是可选端口：多数模块只需要 StatusLine。
type StatusBlock struct {
	Rank int
	// Title 空 = 不打印节标题（用于接在上一段后面的续行）。
	Title string
	// Lines 已排好版，逐行原样输出。
	Lines []string
}

// BlockProvider 允许模块给 `newgate status` 贡献成块的内容，理由见 StatusBlock。
type BlockProvider interface {
	StatusBlocks() []StatusBlock
}

// StatusProvider 允许模块给 `newgate status` 贡献一行，理由同 DiagnosticProvider：
// 模块保留自己的知识，CLI 只负责排版。
//
// 为什么需要它（而不是让 CLI 直接去读某个模块）：`status` 要显示「哪些开关点是
// 非出厂态」，而那份账本归 plugin-manager。CLI 若为了这一行去读它，那一行就会
// 把两个模块焊在一起——「谁的状态谁自己报」是解开这种耦合的唯一办法。
type StatusProvider interface {
	// Status 由**贡献者自己**装所需的配置快照（store.LoadState），界面不传。
	//
	// 为什么不把 state 传进来（2026-09-18）：那会让界面为了转手一份 state 而
	// import config/domain + config/store——「界面认识配置的内部结构」正是这一轮
	// 要拆掉的东西。谁的知识谁自己取，界面只负责循环与排版。
	Status() []StatusLine
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
	PrintChain([]resolve.Step)
	PrintSkips([]resolve.Skip)
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

// Section 是帮助屏上的一个**槽位名**。
//
// 它就是一个字符串，靠**约定**生效，不是闭集（2026-09-18 定）：
//
//   - 界面自己发布一批**通用槽位键**（下面那组常量）：它们是「逻辑上想得通」的
//     大类（接管 / 路由 / 观测 / 维护 …），谁都可以选用；
//   - 通用槽位之外，模块可以**自己起一个名字**——先到先得，最多
//     MaxCustomSections 个，按首次声明的顺序排在通用槽位之后；
//   - 抢不到名额（或者根本没起名字）的一律进 SectionOthers。
//
// 为什么不做成闭集枚举：分类是约定，会随装机长出新成员，硬拒绝的代价是「一个新
// 模块因为用了个新节名，整个 --help 就崩了」——而它只是想被列出来。为什么又要设
// 上限：槽位多了 help 就退化成一堆一行的节，等于没分组。这两条与 pluginmanager
// 的 Type 词汇表是同一个设计（顺序表 + others 兜底）。
//
// 上一版界面反过来维护一张「哪几节是我自己的」的名字清单（coreSection 里那个
// switch）——**界面因为别人的节名而需要被修改**，那正是这一轮要拆掉的东西。
type Section = string

// 通用槽位键。界面发布它们，模块选用；顺序即显示顺序（见 PlanSections）。
const (
	SectionTakeover    Section = "接管"
	SectionRunOnce     Section = "跑一次（不改全局状态）"
	SectionRouting     Section = "路由与配置"
	SectionObserve     Section = "探测与观测"
	SectionMaintenance Section = "维护"
	SectionModules     Section = "模块"
	SectionUI          Section = "界面"

	// SectionOthers 是**兜底槽位**：抢不到名额的、没起名字的都落这里。
	SectionOthers Section = "其它"
)

// generalSections 是界面发布的通用槽位，顺序即显示顺序。SectionOthers 永远最后。
var generalSections = []Section{
	SectionTakeover, SectionRunOnce, SectionRouting,
	SectionObserve, SectionMaintenance, SectionModules, SectionUI,
}

// MaxCustomSections 是通用槽位之外还能容纳的**自定义节**个数。
//
// 有上限是因为节多了 help 就等于没分组；先到先得是因为「谁先声明谁占坑」是唯一
// 不需要界面做价值判断的规则。装到一个新模块抢不到名额时，它落进「其它」照样
// 列得出来，只是不再独占一节。
const MaxCustomSections = 4

// SectionPlan 是一批声明过的节名到**实际显示的节**的映射，以及显示顺序。
//
// 它是一次装配的产物（声明顺序 = 命令注册顺序），所以由界面在渲染时现算。
type SectionPlan struct {
	order []Section
	slot  map[Section]Section
}

// PlanSections 按上面的规则给每个声明过的节名定一个落点。
//
// declared 必须是**稳定的顺序**（命令注册顺序），因为自定义节是先到先得——顺序
// 不定的话同一套装机会渲染出不同的 help，那种不确定性最难查。
func PlanSections(declared []Section) SectionPlan {
	plan := SectionPlan{slot: map[Section]Section{}}
	var customs []Section
	for _, name := range declared {
		if _, done := plan.slot[name]; done {
			continue
		}
		switch {
		case name == "":
			// 没声明 = 没有位置主张，直接进兜底。
			plan.slot[name] = SectionOthers
		case isGeneralSection(name):
			plan.slot[name] = name
		case len(customs) < MaxCustomSections:
			customs = append(customs, name)
			plan.slot[name] = name
		default:
			plan.slot[name] = SectionOthers
		}
	}
	plan.order = append(plan.order, generalSections...)
	plan.order = append(plan.order, customs...)
	plan.order = append(plan.order, SectionOthers)
	return plan
}

// Slot 这个声明实际显示在哪一节。
func (p SectionPlan) Slot(declared Section) Section {
	if s, ok := p.slot[declared]; ok {
		return s
	}
	return SectionOthers
}

// Order 按显示顺序返回所有会出现（或可能为空）的节。
func (p SectionPlan) Order() []Section { return append([]Section(nil), p.order...) }

func isGeneralSection(name Section) bool {
	for _, known := range generalSections {
		if name == known {
			return true
		}
	}
	return false
}

// FlagValue 取 `--名字 值` 或 `--名字=值` 里的值；names 是同一选项的别名，都没有
// 就给空串。与 Arg 同属 argv 解析的公共部分：每个模块各写一遍必然有一遍写错。
func FlagValue(args []string, names ...string) string {
	for i, a := range args {
		for _, name := range names {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, name+"=") {
				return strings.TrimPrefix(a, name+"=")
			}
		}
	}
	return ""
}

// Positional 取第 i 个**位置参数**（跳过 `-` 开头的），越界给空串。
func Positional(args []string, i int) string {
	var seen int
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if seen == i {
			return a
		}
		seen++
	}
	return ""
}

// HelpLine 是命令在 `newgate --help` 里占的那一行。
//
// **节与位置都由命令自己声明，界面不认识任何一条命令**。上一版是反过来的：界面
// 里写死一张 cmd(...) 清单，于是每加一个模块就要回来改界面——命令搬回模块之后
// 界面还在硬编码它们，"搬了"就等于白搬。
type HelpLine struct {
	// Section 归到哪个槽位（见上面的槽位表）。挑不出来就留空，会落进
	// SectionOthers——那是**正确的降级**，不是错误。
	Section Section
	// Rank 决定**节内**位置：小的在前，同 Rank 按 Usage 稳定排序。节的先后由
	// SectionOrder 决定，不受 Rank 影响——否则一个模块调大自己的 Rank 就能把
	// 整节搬走，槽位的意义就没了。
	//
	// 为什么用整数而不是枚举：节的集合随装机变化，枚举必然要改框架。留出空档
	// （10/20/30…）就是留给别人插队的余量。
	Rank int
	// Usage 左列，命令写法。
	Usage string
	// Summary 右列，一句话结论。
	Summary string
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
	// RegisterStatusBlocks 注入 `newgate status` 里的成块内容（表格等），
	// 理由见 StatusBlock。
	RegisterStatusBlocks(BlockProvider) (modules.Release, error)
}

// Handoff 标记这条命令会把控制权交给**别的进程**（包装启动一个客户端）。
//
// 界面拿它做一个判断：这类命令的输出来自那个进程，不该按 newgate 的版式去审计
// 对齐。以前那个判断是界面自己 `detectLaunch` 猜出来的（「第一个 token 是不是
// 已知 agent」）——那需要界面认识 agent 表；现在由命令自己声明，界面只看标记。
type Handoff interface {
	// HandsOff 这条命令是不是把控制权交给别的进程。
	HandsOff()
}

// Unstyled 报告这条命令**这一次调用**的输出不是 newgate 的版式：原始配置文本
// （`profile kv`）、逐字节日志（`logs`）、全屏界面（`tui`）、JSON（`probe --json`）。
// 版式审计跳过它。
//
// **为什么由命令声明而不是界面列名单**（2026-09-18）：界面原来硬编码一张
// `switch args[0]` 的名字表（"__serve", "logs", "alllogs", "tui", "run", "probe",
// "profile"）——那正是「界面认识别人的命令」的又一个面，每加一个命令都要回来改
// 界面。现在命令自己说「我这次不是版式」，界面只问一句。
//
// 它是**动态**的（收 args）：同一条命令可能只有某种用法不排版（`probe --json`，
// 或 `profile kv` 这个子命令）。
type Unstyled interface {
	Unstyled(args []string) bool
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
