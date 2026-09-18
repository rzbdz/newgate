package pluginmanager

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/lib/durarg"
	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/store"
)

// command 是 `newgate plugin`。
//
// **它住在这里而不是 modules/cli**：这是「everything is module」的直接推论——
// 命令是这个模块的用户界面，模块自己提供它，通过 cli.RegisterCommand 注入。
// 2026-09-18 修过一次真实的违规：那一版把它写在 modules/cli 里，理由是「注册
// 命令要 Need(cli)，而 cli 要渲染列表又得 Need(plugin-manager)，成环」——
// 那个环是**自己造的**，因为它同时让 cli 去 Need 本模块。正确的拆法是反过来：
// 让 cli 不必认识本模块（状态行走 cli.RegisterStatus 由本模块自报），于是本
// 模块可以自由地 Need(cli) 并注册自己的命令。
type command struct{ manager Manager }

var (
	_ cliapi.Command        = (*command)(nil)
	_ cliapi.Documented     = (*command)(nil)
	_ cliapi.StatusProvider = (*command)(nil)
)

func (c *command) Names() []string { return []string{"plugin", "plugins"} }

func (c *command) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionModules,
		Usage:   "plugin [模块[.路径]] [on|off] [时长]",
		Summary: "全部模块按分类列出；开关某个模块或某个开关点",
	}
}

// Run 的用法（**args 里没有 "plugin" 这个动词**，分派器已经剥掉了，见
// cliapi.Command 的契约说明）：
//
//	newgate plugin                          按分类列出全部模块
//	newgate plugin <模块>                    展开一个模块：它的开关点与当前状态
//	newgate plugin <模块> on|off [时长]       开/关这个模块的**全部**开关点
//	newgate plugin <模块>.<路径> on|off [时长] 只开/关一个开关点
//
// 列表的枚举源是**组件图**（经本模块合并），不是「谁上报过」。这样没参与开关
// 体系的模块也列得出来，标成「无法 runtime 开关（v1）」——「这个模块没有开关」
// 和「这个模块忘了注册」绝不能长得一样。
func (c *command) Run(host cliapi.Host, args []string) int {
	sub := cliapi.Arg(args, 0)
	switch sub {
	case "", "ls", "list":
		return c.list()
	}
	action := cliapi.Arg(args, 1)
	if action == "" {
		return c.explain(host, sub)
	}
	if action != "on" && action != "off" {
		return host.Die(64, fmt.Sprintf(
			"plugin: 不认识的动词 %q（用法：newgate plugin <模块>[.<路径>] on|off [时长]）", action))
	}
	return c.toggle(host, sub, action == "on", cliapi.Arg(args, 2))
}

// list 按分类分组列出全部模块。
func (c *command) list() int {
	modules := c.manager.Modules()
	st := store.LoadState()

	total, switchPoints := 0, 0
	nameW := 14
	for _, m := range modules {
		total++
		switchPoints += len(m.Switches)
		// 列宽跟着最长的名字走：模块名里有 19 字符的（claudecode-deepseek），
		// 写死宽度会让它把右边的字挤在一起。
		if w := style.VisibleWidth(m.Name); w > nameW {
			nameW = w
		}
	}
	fmt.Println(style.Title("newgate plugin",
		fmt.Sprintf("%d 个模块 · %d 个可运行期开关点", total, switchPoints)))

	for _, typ := range DisplayOrder() {
		var group []Module
		for _, m := range modules {
			if groupOf(m.Type) == typ {
				group = append(group, m)
			}
		}
		if len(group) == 0 {
			continue
		}
		// 组内按名字排序：分组顺序（DisplayOrder）是有意义的，组内顺序不是——
		// 拿拓扑启动顺序排会让人每次都要重新找一遍。
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		// Section 自带前导换行、没有结尾换行：Println 正好补上后者，
		// 于是每组之前恰好一个空行（用 Print 会让标题和第一行粘在一起）。
		fmt.Println(style.Section(string(typ)))
		for _, m := range group {
			printModuleLine(st, m, nameW)
		}
	}

	fmt.Println()
	fmt.Println(style.Hint("展开一个模块：newgate plugin <模块>"))
	fmt.Println(style.Hint("开关一个点：  newgate plugin <模块>.<路径> on|off [5m|forever]"))
	return 0
}

// printModuleLine 渲染列表里的一行：模块名 + 它的开关点概览。
func printModuleLine(st *domain.State, m Module, nameW int) {
	name := style.Pad(m.Name, nameW) + " "
	if len(m.Switches) == 0 {
		fmt.Println("  " + name + style.Dim("无法 runtime 开关（v1）"))
		return
	}
	var parts []string
	for _, sw := range m.Switches {
		parts = append(parts, style.Dim(shortPath(sw.Path))+" "+switchState(st, sw))
	}
	// 第一段跟在模块名后面，其余折到下一行的缩进里——避免一行超过 MaxColumns。
	fmt.Println("  " + name + parts[0])
	for _, p := range parts[1:] {
		fmt.Println("  " + style.Pad("", nameW) + " " + p)
	}
}

// explain 展开一个模块或一个开关点。
func (c *command) explain(host cliapi.Host, target string) int {
	st := store.LoadState()

	if sw, ok := c.manager.Lookup(target); ok {
		return explainSwitch(host, st, sw)
	}

	m, ok := findModule(c.manager, target)
	if !ok {
		return host.Die(65, fmt.Sprintf("没有叫 %q 的模块或开关点（newgate plugin 看清单）", target))
	}
	if len(m.Switches) == 0 {
		fmt.Println(style.Title("newgate plugin "+m.Name, string(m.Type)))
		fmt.Println(style.Rule(72))
		fmt.Println(style.Item(style.Skip, "无法 runtime 开关：这个模块没有上报任何开关点"))
		fmt.Println(style.Hint("v1 只做「模块自己上报」的开关；没上报的模块不能被运行期开关"))
		return 0
	}
	fmt.Println(style.Title("newgate plugin "+m.Name, string(m.Type)))
	fmt.Println(style.Rule(72))
	for _, sw := range m.Switches {
		fmt.Println(style.Item(style.OK, style.Bold(sw.Path)+"  "+switchState(st, sw)))
		fmt.Println("    " + style.Dim(sw.Title))
		fmt.Println("    " + style.Dim("关掉会发生什么："+sw.Why))
		if sw.Danger != DangerSafe {
			fmt.Println("    " + dangerColor(sw.Danger)(fmt.Sprintf("危险级别 %s", sw.Danger)))
		}
	}
	fmt.Println()
	fmt.Println(style.Hint("开关整个模块：newgate plugin " + m.Name + " off"))
	return 0
}

func explainSwitch(host cliapi.Host, st *domain.State, sw Switch) int {
	fmt.Println(style.Title("newgate plugin "+sw.Path, string(sw.Danger)))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(style.OK, sw.Title))
	fmt.Println(style.Item(style.Skip, "现在："+switchState(st, sw)))
	fmt.Println(style.Item(style.Skip, "关掉会发生什么："+sw.Why))
	if sw.Danger == DangerFootgun {
		fmt.Println(style.Item(style.Bad, "这是 footgun：打开/关闭必须带时限，不接受 forever"))
	}
	verb := "off"
	if !sw.Default {
		verb = "on"
	}
	fmt.Println()
	fmt.Println(style.Hint("改它：newgate plugin " + sw.Path + " " + verb + " [时长]"))
	return 0
}

// toggle 改一个或一组开关点。
func (c *command) toggle(host cliapi.Host, target string, on bool, dur string) int {
	switches, err := c.targetsOf(target)
	if err != nil {
		return host.Die(65, err.Error())
	}

	ttl, forever, derr := switchTTL(dur)
	if derr != nil {
		return host.Die(64, derr.Error())
	}

	st := store.LoadState()
	cfg := Parse(rawConfig(st)).Prune()

	var changed []string
	for _, sw := range switches {
		// footgun 不接受「永久」：这是结构性保证的一半（另一半在注册期——
		// footgun 必须声明 TTL > 0，所以哪怕不给时长也有兜底时限）。
		if sw.Danger == DangerFootgun && forever {
			return host.Die(64, fmt.Sprintf(
				"plugin: %s 是 footgun，不接受 forever（给个时长，如 5m）", sw.Path))
		}
		// 出厂态决定这条走哪张表：Default=true 是 kill switch（写 Off 表），
		// Default=false 是 mode（写 On 表）。用户的动作可能等于出厂态（比如
		// 对一个出厂开着的开关点说 on），那就等于撤销这条设定。
		wantOff := sw.Default != on
		if wantOff {
			cfg.Off = setEntry(cfg.Off, sw.Path, effectiveTTL(sw, ttl, forever))
		} else {
			cfg.Off = dropEntry(cfg.Off, sw.Path)
		}
		if !wantOff && !sw.Default {
			cfg.On = setEntry(cfg.On, sw.Path, effectiveTTL(sw, ttl, forever))
		} else {
			cfg.On = dropEntry(cfg.On, sw.Path)
		}
		changed = append(changed, sw.Path)
	}

	raw, merr := cfg.Marshal()
	if merr != nil {
		return host.Die(70, "plugin: 序列化失败: "+merr.Error())
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[StateKey] = raw
	if serr := store.SaveState(st); serr != nil {
		return host.Die(70, serr.Error())
	}
	host.NotifyProxy()

	mark, word := style.OK, "已打开"
	if !on {
		mark, word = style.Warn, "已关闭"
	}
	fmt.Println(style.Item(mark, fmt.Sprintf("%s %s", word, strings.Join(changed, ", "))))
	after := store.LoadState()
	for _, sw := range switches {
		if until := Remaining(after, sw.Path); !until.IsZero() {
			fmt.Println(style.Hint(fmt.Sprintf("  %s 还有 %s 自动恢复",
				sw.Path, durarg.Format(int(time.Until(until).Seconds())))))
		}
	}
	if !on {
		fmt.Println(style.Hint("恢复：newgate plugin " + target + " on"))
	}
	return 0
}

// targetsOf 把用户给的目标解析成一组开关点。带点的是单个路径，不带点的是模块名
// （模块名里没有点——组件名用连字符，所以这条判据不会歧义）。
func (c *command) targetsOf(target string) ([]Switch, error) {
	if strings.Contains(target, ".") {
		sw, ok := c.manager.Lookup(target)
		if !ok {
			return nil, fmt.Errorf("没有叫 %q 的开关点（newgate plugin 看清单）", target)
		}
		return []Switch{sw}, nil
	}
	m, ok := findModule(c.manager, target)
	if !ok {
		return nil, fmt.Errorf("没有叫 %q 的模块（newgate plugin 看清单）", target)
	}
	if len(m.Switches) == 0 {
		return nil, fmt.Errorf("模块 %q 没有上报任何开关点，无法 runtime 开关（v1 的局限）", target)
	}
	return m.Switches, nil
}

func findModule(manager Manager, name string) (Module, bool) {
	for _, m := range manager.Modules() {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

// switchTTL 解析时长参数。空串 = 用开关点自己的默认 TTL；"forever" = 不限时。
func switchTTL(s string) (time.Duration, bool, error) {
	switch s {
	case "":
		return 0, false, nil
	case "forever":
		return 0, true, nil
	}
	d, err := durarg.Parse(s)
	if err != nil {
		return 0, false, fmt.Errorf(
			"plugin: 不认识的时长 %q（支持 30s / 2m / 2min / 1h / forever）", s)
	}
	return d, false, nil
}

// effectiveTTL 这次该记多长时限：用户给了就用用户的，没给就用开关点声明的默认值。
func effectiveTTL(sw Switch, ttl time.Duration, forever bool) time.Time {
	if forever {
		return time.Time{}
	}
	if ttl == 0 {
		ttl = sw.TTL
	}
	if ttl == 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

func setEntry(table map[string]Entry, path string, until time.Time) map[string]Entry {
	if table == nil {
		table = map[string]Entry{}
	}
	table[path] = Entry{Until: until}
	return table
}

func dropEntry(table map[string]Entry, path string) map[string]Entry {
	delete(table, path)
	return table
}

// rawConfig 取 state.json 里 plugin_manager 那一段原始字节。
func rawConfig(st *domain.State) []byte {
	if st == nil {
		return nil
	}
	return st.ModuleConfig[StateKey]
}

// groupOf 把未知分类归到 others。**不报错**：分类是产品概念，会随版本长出新成员，
// 硬拒绝会让一个新模块因为用了个新分类词就把整个列表打崩，而它其实只是想被分到
// 「其它」里。约定俗成 + 留余量。
func groupOf(t Type) Type {
	for _, known := range DisplayOrder() {
		if t == known {
			return t
		}
	}
	return TypeOthers
}

// shortPath 列表里只显示模块名之后的那一段（模块名已经在左边一列了）。
func shortPath(path string) string {
	if i := strings.Index(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// switchState 渲染一条开关点现在的状态。
func switchState(st *domain.State, sw Switch) string {
	until := Remaining(st, sw.Path)
	suffix := ""
	if !until.IsZero() {
		suffix = style.Dim(fmt.Sprintf("（还有 %s）",
			durarg.Format(int(time.Until(until).Seconds()))))
	}
	if sw.Default {
		if Off(st, sw.Path) {
			return dangerColor(sw.Danger)("已关") + suffix
		}
		return style.Dim("开") + suffix
	}
	if On(st, sw.Path) {
		return dangerColor(sw.Danger)("已开") + suffix
	}
	return style.Dim("关") + suffix
}

func dangerColor(d Danger) func(string) string {
	switch d {
	case DangerFootgun:
		return style.Red
	case DangerQuirk:
		return style.Yellow
	default:
		return style.Green
	}
}
