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
	// KindLog 日志流。
	KindLog = "log"
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
	// Apply 为 nil 表示这个概念**只读**（比如带凭据的文件：它的内容是脱敏过的，
	// 写回去就是把 *** 落盘）。
	Apply Applier
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

// Service 是贡献者看到的那一面：登记一个产出函数。
//
// 查询那半边不给出去——它属于界面自己（谁渲染谁读账本）。与 porthub 的 Service
// 只给 Mount 是同一条：给出去的面积越小，能长出来的耦合越少。
type Service interface {
	// Register 登记 source（模块名，报错与分组时点名用）的产出函数，返回撤销。
	Register(source string, read Contributor) (modules.Release, error)
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
	id   int
	name string
	read Contributor
}

func NewRegistry() *Registry {
	return &Registry{}
}

// Register 收下一个贡献者，返回撤销。
//
// 这一步**不调用产出函数**：账本在装配期只知道「谁来报」，不知道「报什么」。
func (r *Registry) Register(name string, read Contributor) (modules.Release, error) {
	if name == "" {
		return nil, i18n.E("view: a contributor registered with no source name", nil)
	}
	if read == nil {
		return nil, i18n.E("view: {source} registered a nil contributor — the interface would show nothing for it",
			i18n.A{"source": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := source{id: r.next, name: name, read: read}
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
// 用户会以为东西变了。真正的版面顺序由前端按 Kind 与它自己的分组决定。
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
