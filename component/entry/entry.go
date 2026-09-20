// Package entry 是**进程入口**这条边的契约：一个进程醒来之后，这次调用该交给谁。
//
// # 为什么它住在内核旁边，而不是某个模块里
//
// 要一个**认不出任何模块**的组合根，就必须有一个双方都看得见的边界类型：组合
// 根在一侧、模块在另一侧，它们不能互相 import，能共同看见的只有内核。这个类型
// 描述的是**进程**（我是怎么被调起来的），不是**业务**（我是谁、我干什么），所以
// 它和 Start/Stop 同级——内核提供形状，产品填内容（见 component.Type 的同款规矩）。
//
// # 为什么不是「app 先问 wrapper、再问 cli」
//
// 那是把分发的判据写死在组合根里：谁是入口、什么条件下归谁，全是模块的知识。
// 2026-09-20 之前 cmd/newgate/main.go 就是这么写的，后果是组合根 import 了
// cli 与 wrapper 两个模块，删掉其中任何一个都无法编译——「用户的模块可以摘掉」
// 这句话在组合根上不成立。
//
// 现在反过来：**入口是贡献者申报的**。每个模块在自己的 Start 里 Register 一个
// Handler 并给出一个 rank（小的先被问），组合根只做一次 Resolve，然后调用它。
//
//	rank 0    wrapper    argv0 是某个被接管的 client（PATH shim 转过来的）
//	rank 1000 cli        默认入口：谁都没认领就是它
//	（将来）  web-daemon 配置里写着 entry=web
//
// 于是「换一个更好的 cli」「让 newgate 直接起一个 web」在组合根上是**零改动**的：
// 换掉 modules/cli 那个目录、或者加一个新模块申报更高的 rank。
package entry

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/buildinfo"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 申报用的三个 rank。小的先被问，**不是魔法数字**，读作优先级区间：
//
//	RankShim      有条件的入口：先问，条件不成立就放过去，让后面的人接手
//	RankPreferred 显式优先：产品/测试主动选定的壳（配置里点名、测试构建里只装它）
//	DefaultRank   兜底：永远返回 true，所以必须排在最后被问
//
// 区间之间的空隙是留给将来的：按环境分流、按配置分流都往中间插，不需要改已有模块。
const (
	RankShim      = 0
	RankPreferred = 500
	DefaultRank   = 1000
)

// Process 是「这次进程调用」的全部事实，也是边界两侧唯一的公共词汇。
//
// 它只有一个来源：组合根在 main 里从 os.Args / os.Environ 与链接期变量装出来。
// 模块不许自己去读 os.Args——那样它就把「进程是怎么被调起来的」这件事又抄了
// 一遍，而这正是本包存在的理由。
type Process struct {
	Argv0      string   // os.Args[0] 的 basename（去 .exe）：`newgate` / `claude` / `newgate-ds`
	Args       []string // os.Args[1:]
	Env        []string // os.Environ() 的原样副本
	Version    string   // 链接期注入；`dev` 表示未注入
	BuildTime  string
	CommitTime string
}

// Handler 是一次入口申报。
type Handler interface {
	// Name 进日志：组合根那句 `[entry] resolve: … → <Name>`。
	Name() string

	// Claims 这次调用归我吗。**必须只读 Process，不许有副作用**——它会被问
	// 在真正分派之前，而且别的申报者也可能被问到。
	Claims(Process) bool

	// Handle 执行这次调用并返回退出码。
	Handle(Process) int
}

// Registry 是入口申报账本。它由 **modules/entry 那个模块**提供（见 modules/entry），
// 那是内核唯一认识、也是唯一摘不掉的模块：用户摘不掉它，所以「入口一定有人管」
// 这条不变式由它保证。
type Registry interface {
	// Register 申报一个入口。rank 小的先被问（见 DefaultRank）。
	Register(h Handler, rank int) (component.Release, error)

	// Resolve 问一遍所有申报者，返回第一个认领的。为什么没认领时 ok=false
	// 而不是 panic：一个只装了业务模块、没装任何界面的图是**合法**的，那时
	// 组合根该报一句人话并退出，不是崩。
	Resolve(p Process) (Handler, string, bool)

	// Handlers 按 rank 顺序列出全部申报者，供 `newgate plugin` 这类诊断枚举。
	Handlers() []Handler
}

// Table 是 Registry 的默认实现。
type Table struct {
	mu       sync.Mutex
	handlers []registration
}

type registration struct {
	handler Handler
	rank    int
	seq     int
}

var _ Registry = (*Table)(nil)

func NewTable() *Table { return &Table{} }

func (t *Table) Register(h Handler, rank int) (component.Release, error) {
	if h == nil {
		return nil, errNilHandler()
	}
	if h.Name() == "" {
		return nil, errEmptyName()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.handlers {
		if r.handler.Name() == h.Name() {
			return nil, errDuplicate(h.Name())
		}
	}
	reg := registration{handler: h, rank: rank, seq: len(t.handlers)}
	t.handlers = append(t.handlers, reg)
	t.sortLocked()
	return func() error {
		t.mu.Lock()
		defer t.mu.Unlock()
		for i, r := range t.handlers {
			if r.seq == reg.seq {
				t.handlers = append(t.handlers[:i:i], t.handlers[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// sortLocked 按 rank 排序，同 rank 保持申报顺序（稳定）。
//
// 稳定性不是细节：`newgate` 这个名字将来可能被两个模块同时认领，此时谁先
// 申报谁赢，而不是由 map 迭代顺序决定——那样的 bug 只在某些机器上出现。
func (t *Table) sortLocked() {
	sort.SliceStable(t.handlers, func(i, j int) bool {
		return t.handlers[i].rank < t.handlers[j].rank
	})
}

func (t *Table) Resolve(p Process) (Handler, string, bool) {
	t.mu.Lock()
	snapshot := append([]registration(nil), t.handlers...)
	t.mu.Unlock()
	asked := make([]string, 0, len(snapshot))
	for _, r := range snapshot {
		if r.handler.Claims(p) {
			return r.handler, i18n.T("claimed by: {chain}",
				i18n.A{"chain": joinAsked(asked, r.handler.Name())}), true
		}
		asked = append(asked, r.handler.Name())
	}
	return nil, i18n.T("no entry claimed this call (asked: {chain})",
		i18n.A{"chain": joinAsked(asked, "")}), false
}

func (t *Table) Handlers() []Handler {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Handler, 0, len(t.handlers))
	for _, r := range t.handlers {
		out = append(out, r.handler)
	}
	return out
}

func joinAsked(asked []string, winner string) string {
	out := ""
	for i, name := range asked {
		if i > 0 {
			out += " → "
		}
		out += i18n.T("{name} did not claim", i18n.A{"name": name})
	}
	if winner != "" {
		if out != "" {
			out += " → "
		}
		out += winner
	}
	if out == "" {
		return i18n.T("(nobody registered)", nil)
	}
	return out
}

// Capability 是入口申报账本的端口身份。它由 modules/entry 提供（见 modules/entry）。
//
// 组合根（app）也 import 本包——它是唯一能这么做的模块侧契约（见本包开头那段）。
var Capability = component.NewCapability[Registry]("entry")

// ---- 申报期能犯的两种错 ----
//
// 它们在这里而不是调用点：申报是**模块自己的编程错误**（重名、空名、nil），
// 必须当场返回错误让那个模块的 Start 失败并回滚，而不是等到分派时才炸。
//
// 三条都写成**函数**而不是包级变量：包级变量在 init 期求值，而 i18n 的译文是
// 装配期才装上的（modules/locale 在 Start 里 Install），init 期取到的永远是
// 源语言那一句——一个只在非英文环境下才显形的 bug。
func errDuplicate(name string) error {
	return i18n.E("entry: a handler with this name is already registered: {name}",
		i18n.A{"name": name})
}

func errNilHandler() error {
	return i18n.E("entry: handler must not be nil", nil)
}

func errEmptyName() error {
	return i18n.E("entry: handler must have a Name()", nil)
}

// MakeProcess 把「进程是怎么被调起来的」装成一个 Process，并把链接期版本信息
// 落到 buildinfo 叶子上（守护进程的启动日志、doctor 的版本行都读它）。
//
// **只有组合根该调它**，而且只在 main 里调一次。它做两件必须成对发生的事：
// 归一 argv0（basename、剥 .exe、剥路径）、注入版本——两者都是「这个二进制
// 这一趟的事实」，模块自己各读各的就会漂移。
func MakeProcess(argv []string, env []string, version, buildTime, commitTime string) Process {
	buildinfo.Set(version, buildTime, commitTime)
	name := ""
	var args []string
	if len(argv) > 0 {
		name = strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
		args = argv[1:]
	}
	return Process{
		Argv0:      name,
		Args:       args,
		Env:        env,
		Version:    version,
		BuildTime:  buildTime,
		CommitTime: commitTime,
	}
}
