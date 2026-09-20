// Package view 是模块向 web 界面贡献控制面时实现的那组接口，以及那份契约的身份。
//
// 它与 modules/cli/extension 是**同一条规矩的两份实现**：模块保留自己的知识，
// 界面只负责编排与渲染。理由也一样——界面（不管是 CLI 还是浏览器）一旦为了显示
// 某样东西去读某个模块的内部，那两样东西就焊在一起了，而「谁的状态谁自己报」是
// 解开它的唯一办法。
//
// # 为什么契约要下沉成叶子包
//
// web 界面是个重包（它 import gateway 的 metrics、config 的 store、HTTP 那一套）。
// 模块若为了拿到「概念」这个类型去 import 它，就把这些全拖进自己的依赖里，于是
// 谁 import 了那个模块就再也无法被 gateway/config 的测试引用——成环。所以契约住
// 这里，箭头是 `modules/* → lib/view → component`。
//
// # 账本在哪
//
// **账本（Registry）由提供界面的那个模块自己的 service 持有**（发行版的
// modules/web-dashboard），与 cli 那边一模一样：贡献者拿到的是 Register，
// 界面在自己的 service 上问它要数据。所以 web-dashboard 不认识任何模块，
// 加一个模块的界面也不需要改它——更不用改前端（前端只认 Kind）。
//
// 登记时除了产出函数还要报一个**栏目名**（Section）：界面把概念按来源分栏，
// 而「这一栏叫什么」是模块自己的事——名字若住在界面里，加一个模块就得改一次
// 界面，那正是上面那条规矩要避免的。名字在**快照那一刻**求值，理由见 Section。
//
// # 一个概念 = 一份文件的全部知识
//
// 概念不只报**数据**，还带 `Apply`：怎么写回磁盘是**拥有那份数据的人**的知识。
// 这一条是这套东西能站住的关键——只要界面自己动手写文件，它就必然要知道文件
// 格式、字段语义、以及哪些字段不能碰，那正是「BFF 认识 config 的内部」。
//
// # 产出是**有人来问的时候**才发生的
//
// 注册（Register）只登记一个产出函数，不读盘、不解析、不算指标。产出函数要等到
// 真的有人来看界面时才被调用。
//
// 这不是性能洁癖，是这套东西的正确性问题：**每一个 `newgate ...` 进程都会跑一遍
// 模块的 Start**，而其中绝大多数（敲一条命令、进一个 shell、跑一次体检）根本
// 没有人会看界面。把「读全部配置、解析每个 profile、解析每个档位绑到哪家 provider」
// 放在注册那一刻，就是让每一次 CLI 调用都白付一遍这笔账——而它买到的只是「一个
// 没有人会打开的快照」。第一版就是那么写的（Start 里直接读出全部数据塞进概念），
// 被删掉了。
//
// 顺带买到的第二件事：界面上的刷新是真的刷新。产出函数每次重跑，CLI 刚建的档位
// 文件、刚改过的 providers.json 都会出现在下一次快照里——装配期数一遍的名单迟早
// 会数错（daemon 是长命的，而文件是 CLI 随时能动的）。
package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Kind 是渲染方式的名字。**前端只认这几种**：加一个新模块的界面不需要改前端，
// 除非它带来的是一种新的**形状**（那时候两边都要动，这是诚实的代价）。
const (
	// KindMapping 档位/候选这种「列表套列表」的绑定编辑器：每一项是一组有序候选，
	// 顺序就是 fallback 顺序。
	KindMapping = "mapping-editor"
	// KindCode 源文件（带语法高亮的编辑器）。
	KindCode = "code"
	// KindToggles 一组开关 + 选择器（每个都带一句为什么）。
	KindToggles = "toggles"
	// KindSeries 计数器/指标。
	KindSeries = "series"
	// KindTable 只读的表格。
	KindTable = "table"
	// KindRecords 一组**结构化的记录**：每条几个字段，字段自己声明类型。
	//
	// 它和 KindToggles 的分工：toggles 是「一组同构的布尔/单选」，每条就一个值；
	// 这里每条是一张**小表单**（provider 的 base_url、protocol、key…），条数还会
	// 增减。两者都需要，因为把它们塞进一个形状的结果是其中一个从此别扭。
	KindRecords = "records"
	// KindLog 日志流。
	KindLog = "log"
)

// Table 是 KindTable 的数据形状：几列 + 几行，**只读**。
//
// 形状定在这里、而不是各模块自己拼一个 map 的原因与 Kind 那组常量一样：前端
// 只认这一种摆法。谁的表都长这样，前端就只有一个渲染器。
//
// 为什么行是「按列名索引的 map」而不是数组：列的顺序是**展示**的事（前端可以
// 按窄屏重排、将来可以给用户拖动），而格子和列的对应关系是**数据**的事。用
// 数组下标把它们绑死，前端一动列顺序，所有格子就串位了——那种错还会看起来很
// 正常（每一格都有值，只是值不对）。
type Table struct {
	Columns []Column `json:"columns"`
	// Rows 的每一项是「列 ID → 格子」。缺的列渲染成空白，不是错误：一张表的
	// 行本来就可以有稀疏的字段（比如「只计数没摘牌」的行没有冷却时刻）。
	Rows []map[string]Cell `json:"rows"`
}

// Column 是表头的一列。
type Column struct {
	// ID 是这一列的**身份**：行里的格子按它索引。稳定的机器标记，不翻译。
	ID string `json:"id"`
	// Label 是给人看的表头（可以翻译、可以改）。
	Label string `json:"label"`
	// Align 是 "right" 时右对齐。数字列要它——左对齐的数字没法竖着比大小。
	Align string `json:"align,omitempty"`
}

// Cell 是一格。
type Cell struct {
	Text string `json:"text"`
	// Tone 是这一格的颜色语义，取 cli 那套 ok/warn/bad/skip 里的前三个
	// （没有 "skip"：那是「这一步没跑」的意思，只对流程有意义，表格里用不上）。
	//
	// 空串 = 不着色。**不许**在这里传 ANSI：颜色是渲染层的事，内核只给语义，
	// 否则同一份数据在网页与终端上就得各写一遍转义。
	Tone string `json:"tone,omitempty"`
}

// Tone 的取值。前端按这几个词上色，其余一律当没给。
const (
	ToneOK   = "ok"
	ToneWarn = "warn"
	ToneBad  = "bad"
)

// Records 是 KindRecords 的数据形状：一组记录，每条几个具名字段。
//
// 字段的**类型**（text / select / secret / lines）由贡献者声明：它知道这个字段
// 是一个封闭集合（协议只有两家）、还是一个自由文本、还是不该给浏览器看的凭据。
// 前端只按这四个词渲染，不认识任何具体字段——「加一个模块的面不用改前端」在
// 这里与别处是同一条规矩。
type Records struct {
	// File 是这份记录集**写的是哪一份文件**，相对配置根（空 = 它不对应任何可编辑
	// 的文件）。界面拿它把「控件」与「原文」两半配成一对并排（见 nav.ts 的 fileOf：
	// 所有带这个字段的形状都适用，不是给某一种 Kind 开的口子）。
	File  string   `json:"file,omitempty"`
	Items []Record `json:"items"`
	// Base 是这份文件加载时的基线（内容哈希，见 store.WriteIfUnchanged）。
	// 可写的记录集必须有它：没有基线，保存就无从判断「我手里这份是不是最新的」，
	// 而那正是「命令行刚改了同一个文件」的唯一防线（见 Concept.Apply 的注释）。
	// 只读的记录集留空。
	Base string `json:"base,omitempty"`
	// CanAdd 为 true 时界面给一个「新增一条」的按钮，AddLabel 是它的字。
	// 为什么不是所有 Records 都能加：有的记录集合由别的东西决定（比如「这个构建
	// 装了哪些模块」），给它一个加了没用的按钮就是骗人。
	CanAdd   bool   `json:"can_add,omitempty"`
	AddLabel string `json:"add_label,omitempty"`
}

// Record 是一条。
type Record struct {
	// ID 是这条**此刻**的身份（改名就是换一条：ID 变了）。空的 = 界面上新加的，
	// 贡献者按「新增」处理。它同时是 Apply 回传时用来对号的东西。
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Fields []Field `json:"fields"`
	// Removable 为 true 时界面给一个删除按钮。刻意**不是**默认开：删掉一条记录
	// 通常意味着删掉磁盘上的一段配置，那种动作该由贡献者显式说「这条可以删」，
	// 而不是由「它是个 record」推出来。
	Removable bool `json:"removable,omitempty"`
	// Base 是**这一条**代表的那份文件加载时的基线，见 Records.Base。有的记录集
	// 一条对应一份文件（如「这些档位文件」），删这一条就是删那份文件，得用这条
	// 自己的版本来判断「没被别人抢先动过」。界面原样回传，不解释。
	Base string `json:"base,omitempty"`
}

// Field 是一条记录里的一个字段。
type Field struct {
	// ID 是字段名（Apply 回传时的键）。机器标记，不翻译。
	ID string `json:"id"`
	// Label 是给人看的字段名（可以翻译）。
	Label string `json:"label"`
	// Kind 决定怎么渲染：text（普通文本）/ select（Options 里挑一个）/
	// secret（凭据：**值不在快照里**，界面显示占位符，敲了才改）/
	// lines（多行，一行一项，用于「一组名字」那种列表）。
	Kind string `json:"kind"`
	// Value 是当前值。**secret 字段的 Value 恒为空串**——见下面的注释。
	Value   string   `json:"value,omitempty"`
	Options []string `json:"options,omitempty"`
	// Placeholder 是空值时的提示（界面画在输入框里）。
	Placeholder string `json:"placeholder,omitempty"`
	// Why 是一句话说明这个字段影响什么（鼠标悬停时给）。
	Why string `json:"why,omitempty"`
}

// 字段类型的取值。
const (
	FieldText   = "text"
	FieldSelect = "select"
	// FieldSecret 是**凭据**：值不会出现在快照里，界面拿到的是一个空串加一句
	// 占位提示。空串回传 = 「别动它」，敲了新值才是改。
	//
	// 为什么不是「脱敏成 *** 再让界面改」：界面手里的原文已经被换成 *** 了，
	// 它写回来就是把 *** 落盘——那是数据丢失，比不能改严重得多（同一个理由见
	// modules/config/view.go 里 Redacted 那段）。所以这里的做法是**值根本不出去**：
	// 浏览器没有的东西，就不可能被原样写回来。
	//
	// 判据是「这东西泄露了会不会疼」，不是「它是不是密码」：api_key 是，
	// base_url 不是。
	FieldSecret = "secret"
	// FieldLines 是多行文本，一行一项（「这家的模型清单」那种）。
	FieldLines = "lines"
)

// Applier 把这个概念的一次修改落盘。
//
// `edit` 的形状由**这个概念自己**定义（每个概念的应用者自己解释），界面不解析
// 它，只负责把前端产出的那坨 JSON 原样递回来。`base` 是加载时拿到的基线
// （内容哈希，见 store.Revision）：实现方必须先比对再写，不一致就返回 *Conflict。
//
// 返回的新基线供前端续着改——保存一次之后不该逼用户刷新页面。
type Applier func(edit json.RawMessage, base string) (newBase string, err error)

// Concept 是一个模块贡献给界面的东西。
type Concept struct {
	// ID 是**稳定身份**：排序、持久化、以及「这次修改针对谁」都用它。
	//
	// 不用标题当身份：标题是给人看的，会翻译、会改措辞，而身份一变，前端的
	// 本地状态（展开的卡片、正在编辑的草稿）就全部对不上了。
	ID string
	// Kind 决定前端怎么渲染（见上面的常量）。
	Kind string
	// Title 是给人看的名字（可以翻译、可以改）。
	Title string
	// Source 是谁贡献的（模块名）。**由账本填写**，贡献者不用自己报——报错了
	// 就是替别人背锅，而账本本来就知道这条是谁登记的。
	Source string
	// Data 是给前端渲染的结构化数据，形状由 Kind 约定。
	Data any
	// Broken 非空表示这个概念**此刻读不出来**（文件被删了、JSON 坏了）。
	//
	// 界面照常渲染一张卡片，上面写这句话。为什么不直接把它从快照里去掉、
	// 或者让整次快照失败：前者是静默丢功能（用户以为那个档位不存在），后者是
	// 一个坏文件让整个界面白屏。两种都比「这个东西坏了，原因是 X」差。
	Broken string
	// Live 表示**这一面的数据会自己变**，界面该每隔几秒重新问它一次。
	//
	// 由贡献者声明，不由 Kind 推断（2026-09-20 之前是后者：界面把 series 与 log
	// 当成「便宜、可以常问」的同义词）。那条推断有两个毛病：一是把**成本**这个
	// 贡献者才知道的事，写成了渲染形状的附庸；二是没给别的 Kind 留口子——一张
	// 显示「还有多久自动关闭」的表，页面开着不动它就永远停在那个数字上。
	//
	// 声明的判据是**读一次贵不贵**，不是数据变不变：计数器、日志尾巴、几个开关
	// 的当前值都是内存里读一把，可以常问；config 那一面要重读并重新解析每一份
	// profile 与每一个源文件，就不行（见 Registry.Snapshot 的注释）。
	//
	// **粒度是源，不是这一条概念**：界面刷的是「有谁声明了 Live」的那些**源**，
	// 而问一个源就是问它全部的贡献者。所以同一个源里的概念得一起便宜——一条
	// 便宜的声明会把同源那几位一起拖进几秒一次的轮询里。今天只有 gateway 与
	// claudecode 两个源声明，各自的贡献者都是内存里读一把。
	Live bool
	// Apply 为 nil 表示这个概念**只读**（比如带凭据的文件：它的内容是脱敏过的，
	// 写回去就是把 *** 落盘）。
	Apply Applier
	// Order 是**同一节里谁排前面**（小的在前，同值按 ID 排）。
	//
	// 为什么由贡献者说，而不是按 ID 字母序：字母序会把「一次装完就要配的基本项」
	// （上游、全局设置）排到「一天天加出来的那一堆」后面——`config.profile.*` 字母
	// 序在 `config.providers` 之前，于是左栏第一屏全是档位文件，而上游配置在第一屏
	// 之外。那不是排版偏好：先配上游才选得出档位绑定，顺序与操作的先后是同一件事。
	Order int
	// Group 是**左栏分组**：同组的卡在竖栏里归到一个标题下（成员缩进一级）。
	//
	// 空串 = 不分组（自己一档）。它只影响排列，不影响任何身份——分组变了不会让
	// 谁的 ID 跟着变。典型用法是「一族档位」：`claude` 与 `claude-cheap` 同组。
	Group string
}

// Conflict 是「你手里那份已经不是最新的了」。
//
// 它带**两边的原文**，因为只有摆出来用户才能决定怎么办：他的改动与别人的改动
// 可能只是碰巧撞上了同一段，也可能南辕北辙。「保存失败，请重试」把这件事交给
// 用户去猜，而猜错的代价是丢数据。
type Conflict struct {
	Concept string
	Path    string
	Base    string // 你加载时那份
	Current string // 磁盘上现在这份
	Yours   string // 你这次要写进去的
	Theirs  string // 磁盘上现在这份的原文（等于 Current 那份的内容，给人看）
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("view: %s changed on disk since it was loaded (%s)", c.Path, c.Concept)
}

// Contributor 是「这个模块此刻贡献什么」这件事本身。
//
// 它被调用的时机只有一个：有人来问界面要数据。返回值里的每个概念都该是**当下**
// 的样子（该读的文件现在读、该算的指标现在算）。
//
// 返回 error 是给「整组都产不出来」用的（比如配置目录整个读不了）。单个概念
// 自己的毛病不要走这里——那会连带把别人的面也弄没，用 Concept.Broken。
type Contributor func() ([]Concept, error)

// Section 是「这一栏」的展示面：它的名字。
//
// 名字为什么是个函数，而不是登记时算好的一个字符串：**登记发生在各模块的 Start
// 里，而那时候语言层装好了没有是没有保证的**。业务模块不依赖 modules/locale
// （依赖它的只有发行版的 modules/i18n，它要把自带的目录追加进内核已经装好的
// 那一份），所以「谁先 Start」不归我们管；在 Start 里求值的标题会冻在源语言上，
// 而**没有任何东西会因此变红**——中文界面上那一栏就一直叫 Configuration。
//
// 求值挪到快照那一刻（Registry.Sections），就与概念标题同一时机、同一门语言：
// 概念标题一直是那么做的（见 modules/config/view.go 里 `Title: i18n.T(...)`
// 都在产出函数里），这条只是把栏名拉齐到同一个规矩上。
type Section struct {
	Title func() string
}

// Title 造一个栏目名。传进来的是个**闭包**，不是译好的字符串：
//
//	v.Register("config", view.Title(func() string { return i18n.T("Configuration", nil) }), concepts)
//
// 这个形状有两个好处，都不是洁癖：
//
//   - 类型上就写不出「登记时把标题翻好」那种错（想传字符串编译不过），而那个
//     错是静默的——英文标题在中文界面上看着只像「还没翻」，不像一条 bug；
//   - i18n 的扫描器照常看得见那句 msgid。它认的是源码里的 `i18n.T(...)` 调用，
//     而闭包体里的调用同样在这棵 AST 里，所以**不需要教扫描器认识 view.Title**。
//     反过来若把 msgid 当字符串收进来（`view.Title("Configuration")`），那句
//     英文就谁也扫不到：账本里没有它、check 全绿、中文目录里永远缺一条。
func Title(f func() string) Section { return Section{Title: f} }

// SectionInfo 是栏目列表里的一行。
//
// Source 是机器标记（前端拿它跟概念对上、也拿它写进 URL），Title 是给人看的。
// 与概念一样：标题可以翻译、可以改措辞，而身份不能。
type SectionInfo struct {
	Source string `json:"source"`
	Title  string `json:"title"`
}

// Service 是贡献者看到的那一面：登记一个产出函数。
//
// 查询那半边不给出去——它属于界面自己（谁渲染谁读账本）。与 porthub 的 Service
// 只给 Mount 是同一条：给出去的面积越小，能长出来的耦合越少。
type Service interface {
	// Register 登记 source（模块名，报错与分组时点名用）与它的栏目名，外加产出
	// 函数。返回撤销。
	Register(source string, section Section, read Contributor) (modules.Release, error)
}

// Capability 是「这个界面在这个进程里」这件事本身。
//
// 提供者是**界面模块**（发行版的 modules/web-dashboard）；贡献者是各业务模块
// （config 报档位与文件、gateway 报计数器……），它们 Optional 依赖它：界面在
// 就注册进去，界面不在就跳过——模块功能一个都不少，只是没有 web 入口。
var Capability = modules.NewCapability[Service]("view")

// ---------- 账本 ----------

// Registry 是贡献者的账本。零值不可用，用 NewRegistry。
//
// 它住在叶子包里（而不是界面模块内部）只有一个理由：**它能被单测**。账本的行为
// （重名、撤销、并发）与渲染无关，而渲染那半边的测试要拖起 HTTP 与嵌入资源。
type Registry struct {
	mu      sync.RWMutex
	next    int
	sources []source
}

type source struct {
	id    int
	name  string
	title func() string // Register 拒绝 nil，所以这里一定非空
	read  Contributor
}

func NewRegistry() *Registry {
	return &Registry{}
}

// Register 收下一个贡献者，返回撤销。
//
// 这一步**不调用产出函数**：账本在装配期只知道「谁来报」，不知道「报什么」。
func (r *Registry) Register(name string, section Section, read Contributor) (modules.Release, error) {
	if name == "" {
		return nil, i18n.E("view: a contributor registered with no source name", nil)
	}
	if section.Title == nil {
		// 报错而不是兜个名字：栏目名缺失是**界面上一栏没有标题**，而那一刻离这里
		// 隔着一次 HTTP 与一次渲染，谁也不会顺着摸回来。这里红一下最便宜。
		return nil, i18n.E("view: {source} registered with no section title — the sidebar would have "+
			"nothing to label its column with (use view.Title)", i18n.A{"source": name})
	}
	if read == nil {
		return nil, i18n.E("view: {source} registered a nil contributor — the interface would show nothing for it",
			i18n.A{"source": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := source{id: r.next, name: name, title: section.Title, read: read}
	r.next++
	r.sources = append(r.sources, s)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		// 撤销只撤自己那一条（按登记的序号认人，不按名字）：同一个模块名重复登记
		// 是允许的，而「后来者也叫这个名字」不该被前一个的撤销带走。
		for i, cur := range r.sources {
			if cur.id == s.id {
				r.sources = append(r.sources[:i], r.sources[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// Sections 列出**登记过的全部栏目**（不只是此刻有概念的），按 Source 排序。
//
// 为什么不复用「从概念里取 source」：一个源此刻可能一条概念都产不出来——配置
// 目录整个读不了、omo 还没接管过、插件一个开关点都没上报。那些栏目仍然该在
// 侧栏里有一个位置：它从列表里消失，用户会以为那个模块不存在，然后去别处找。
//
// 它**不调用任何产出函数**（只取登记的栏目名），所以随时问都很便宜，界面可以
// 在每次快照里捎上它。栏名在这里求值——快照这一刻的语言，而不是登记那一刻的
// （见 Section 的注释）。
func (r *Registry) Sections() []SectionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SectionInfo, 0, len(r.sources))
	for _, s := range r.sources {
		title := ""
		if s.title != nil {
			title = s.title()
		}
		if strings.TrimSpace(title) == "" {
			// 兜底而不是报错：栏名是一句展示文案，为它让整次快照失败，代价是
			// 整个界面白屏——比一栏的标题写成 source 名严重得多。
			title = s.name
		}
		out = append(out, SectionInfo{Source: s.name, Title: title})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// Snapshot 问一遍贡献者，返回这一刻的概念（按 Source, ID 排序）。
//
// 不带参数 = 问全部。带名字 = **只问这几位**——这条不是优化洁癖，是刷新语义的
// 一部分：界面每几秒刷一次的是计数器与日志（那几位产出很便宜），而配置那一位要
// 重读并重新解析每一份 profile、每一个源文件。让「刷一下计数器」顺带把整个配置
// 目录重读一遍，是把成本花在没人看的地方。
//
// 名字不认识时**不报错**（那位没装、或者刚被关掉）：刷新是后台行为，为一个已经
// 不在的贡献者让整次刷新失败，界面会停在上一帧。
//
// 排序是给**诊断输出**用的：同一份装配跑两次，界面上的卡片顺序必须一样，否则
// 用户会以为东西变了。版面顺序由前端决定，而它今天是**按 Source 分组**（每个源
// 一个小标题，组内按 ID）——Kind 只决定一张卡片怎么画，不决定它排在哪儿。
// （2026-09-20 更正：这里原来写的是「按 Kind 分组」，前端从来不是那么做的。）
func (r *Registry) Snapshot(only ...string) ([]Concept, error) {
	r.mu.RLock()
	sources := append([]source(nil), r.sources...)
	r.mu.RUnlock()
	if len(only) > 0 {
		want := make(map[string]bool, len(only))
		for _, name := range only {
			want[name] = true
		}
		kept := sources[:0:0]
		for _, s := range sources {
			if want[s.name] {
				kept = append(kept, s)
			}
		}
		sources = kept
	}

	var out []Concept
	owner := map[string]string{}
	for _, s := range sources {
		concepts, err := s.read()
		if err != nil {
			return nil, i18n.Ef(err, "interface: {source} could not produce its view: {err}",
				i18n.A{"source": s.name, "err": err})
		}
		for _, c := range concepts {
			c.Source = s.name
			// 重名当场报错，不做「后来者覆盖」：两个模块各自以为自己在报同一个 ID
			// 的时候，界面上只会显示其中一个，而另一个的修改点永远点不到——那是
			// 静默丢功能，跟前缀冲突（porthub）是同一类坑。
			//
			// 这条以前发生在注册那一刻，现在只能在这里——因为「有哪几个概念」本身
			// 就是产出函数说了算的。代价是它从装配期挪到了第一次有人看界面时；
			// 换来的是装配期一次盘都不读（见包注释）。
			if prev, dup := owner[c.ID]; dup {
				return nil, i18n.E("view: concept {id} is contributed by both {a} and {b} — "+
					"two concepts on one id means only one of them is ever reachable from the interface",
					i18n.A{"id": c.ID, "a": prev, "b": s.name})
			}
			if c.ID == "" {
				return nil, i18n.E("view: {source} contributed a concept with no id", i18n.A{"source": s.name})
			}
			if c.Kind == "" {
				return nil, i18n.E("view: concept {id} ({source}) has no kind — "+
					"the interface would not know how to render it",
					i18n.A{"id": c.ID, "source": s.name})
			}
			owner[c.ID] = s.name
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		// Order 在前，ID 兜底：没声明 Order 的贡献者（今天大多数）行为与以前
		// 一模一样（全是 0，于是按 ID 排），声明了的就能把自己排到前面去。
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ErrUnknownConcept 表示「这个 ID 现在没人报」。
//
// 界面拿它区分 404（前端手里那份快照过期了，重载就好）与 500（某种真的坏了）。
var ErrUnknownConcept = errors.New("view: no such concept")

// Get 取一个概念（界面按 ID 找「这次修改针对谁」）。
//
// 它走的是 Snapshot：产出函数会**再跑一遍**。这是有意的——Apply 闭包由产出函数
// 现场构造（它得知道文件现在的路径与基线），缓存一份旧的等于拿过期知识去写盘。
func (r *Registry) Get(id string) (Concept, error) {
	all, err := r.Snapshot()
	if err != nil {
		return Concept{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return Concept{}, fmt.Errorf("%w: %s", ErrUnknownConcept, id)
}
