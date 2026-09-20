package pluginmanager

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 本模块贡献给 web 界面的东西：**运行期开关点**。
//
// 这是「开关账本」最自然的第二个入口——`newgate plugin` 是命令行那一份，浏览器
// 这一份把同一批开关点画成开关。两份入口、**一份写语义**：都走 SetSwitch，所以
// 出厂态决定走哪张表、动作等于出厂态就是撤销、footgun 不收永久，这几条不会在
// 两个界面上给出不同答案（见 set.go 的说明）。
//
// 为什么分成两个概念（可写的 / 只读的）而不是一张卡片里混着：概念的可写性是
// **整个概念**的属性（前端据此决定给不给保存），而 footgun 恰恰不能在这里随手开
// 关——它必须带时限。硬塞进同一个概念的结果是界面上给了一个按下去会失败的开关，
// 那比不给更糟。所以 footgun 单列一张只读卡片，写明它为什么只读、该用什么命令。

type switchItem struct {
	ID     string `json:"id"`     // 开关点路径（StateKey 里的键）
	Label  string `json:"label"`  // 人话标题
	Kind   string `json:"kind"`   // 界面渲染用：全是 bool
	Value  bool   `json:"value"`  // **当前生效**的状态（出厂态 + 用户的设定）
	Why    string `json:"why"`    // 关掉会发生什么
	Group  string `json:"group"`  // 模块名（界面拿它分组）
	Danger string `json:"danger"` // safe / quirk / footgun
	Until  string `json:"until,omitempty"`
}

type switchesData struct {
	File  string       `json:"file"`
	Base  string       `json:"base"`
	Items []switchItem `json:"items"`
}

// viewConcepts 产出这一刻的两张开关卡片。
//
// 它每次被调用都重新算一遍「现在开着没有」——用户刚在命令行改过的开关，界面上
// 刷新一下就对了（见 lib/view 的包注释：产出发生在有人来问的那一刻）。
func viewConcepts(m Manager) ([]view.Concept, error) {
	st := store.LoadState()
	file := paths.StateFile()
	base := store.Revision(file)

	var writable, footguns []switchItem
	for _, mod := range m.Modules() {
		for _, sw := range mod.Switches {
			it := switchItem{
				ID: sw.Path, Label: sw.Title, Kind: "bool",
				Value: switchOn(st, sw), Why: sw.Why,
				Group: mod.Name, Danger: string(sw.Danger),
			}
			if until := Remaining(st, sw.Path); !until.IsZero() {
				it.Until = until.Format(time.RFC3339)
			}
			if sw.Danger == DangerFootgun {
				footguns = append(footguns, it)
				continue
			}
			writable = append(writable, it)
		}
	}
	if len(writable) == 0 && len(footguns) == 0 {
		// 一个开关点都没上报（比如骨架发行版）：不报概念，而不是报一张空卡片。
		// 空卡片在界面上看起来像「有东西没加载出来」。
		return nil, nil
	}

	var out []view.Concept
	if len(writable) > 0 {
		out = append(out, view.Concept{
			ID: "plugin-manager.switches", Kind: view.KindToggles,
			Title: i18n.T("Runtime switches", nil),
			Data:  switchesData{File: file, Base: base, Items: writable},
			Apply: func(edit json.RawMessage, base string) (string, error) {
				return applySwitches(m, edit, base)
			},
		})
	}
	if len(footguns) > 0 {
		// 只读（没有 Apply）。为什么不给个「带时限」的开关：时限要有人选，而这一版
		// 没有那个界面；给一个按下去必定失败的开关比不给更糟。命令写在 why 里。
		for i := range footguns {
			footguns[i].Why = i18n.T("{why} — footguns need a time limit; use: newgate plugin {path} on 5m",
				i18n.A{"why": footguns[i].Why, "path": footguns[i].ID})
		}
		out = append(out, view.Concept{
			ID: "plugin-manager.footguns", Kind: view.KindToggles,
			Title: i18n.T("Switches that need a time limit", nil),
			Data:  switchesData{File: file, Base: base, Items: footguns},
		})
	}
	return out, nil
}

// switchOn 是**当前生效**的状态：出厂开着的（kill switch）看 Off 表，出厂关着的
// （mode）看 On 表。判据与 CLI 的 switchState 一字不差——两边说不一样的话，
// 用户会以为其中一个界面在骗他。
func switchOn(st *domain.State, sw Switch) bool {
	if st == nil {
		return sw.Default
	}
	if sw.Default {
		return !Off(st, sw.Path)
	}
	return On(st, sw.Path)
}

// applySwitches 把界面这次交上来的整张表写进 state.json。
//
// edit 的形状：`{"<开关点路径>": true|false}`——界面把手里的**全部**项交回来
// （见前端 Toggles 的注释），所以这里逐条应用，等于「把每个开关设成这个样子」。
// 未变的那几条是无害的幂等写（见 SetSwitch）。
func applySwitches(m Manager, edit json.RawMessage, base string) (string, error) {
	var patch map[string]bool
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the switches in this request are not readable: {err}", i18n.A{"err": err})
	}
	// 排序只为让错误可复现：同一个坏请求跑两次该报同一条。
	paths_ := make([]string, 0, len(patch))
	for p := range patch {
		paths_ = append(paths_, p)
	}
	sort.Strings(paths_)

	st := store.LoadState()
	for _, p := range paths_ {
		sw, ok := m.Lookup(p)
		if !ok {
			// 界面手里的开关点已经不在了（模块被换掉、产物换了一份）。说清楚比
			// 静默跳过强：静默跳过的那天，用户会以为自己按的开关生效了。
			return "", i18n.E("no switch point named \"{path}\" — the page is probably stale, reload it",
				i18n.A{"path": p})
		}
		if err := SetSwitch(st, sw, patch[p], 0, false); err != nil {
			return "", err
		}
	}

	b, err := store.StateBytes(st)
	if err != nil {
		return "", err
	}
	return writeThrough("plugin-manager.switches", paths.StateFile(), base, b)
}

// writeThrough 与 modules/config/view.go 里那一段是同一件事：把 store 的
// 「基线不对」翻译成界面契约的 Conflict。
//
// 六行，两边各一份，是**刻意**的：CAS 的语义（比对什么、怎么锁、写多原子）在
// store 里只有一份，这里只是把它的失败换个形状。反过来让 store 认识 view 契约，
// 才是把界面的知识长进数据层。
func writeThrough(conceptID, file, base string, data []byte) (string, error) {
	rev, err := store.WriteIfUnchanged(file, base, data)
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return "", &view.Conflict{
			Concept: conceptID,
			Path:    file,
			Base:    stale.Base,
			Current: stale.Current,
			Yours:   string(data),
			Theirs:  string(stale.Disk),
		}
	}
	return rev, err
}
