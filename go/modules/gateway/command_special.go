package gateway

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/store"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// specialCommand 是 `newgate st`：special_treatment 插件层的清单与开关。
//
// **它住在这里而不是 modules/cli**：这一层的插件是网关数据面的一部分，插件名、
// Why、开关语义全是 gateway 自己的知识。2026-09-18 之前它写在 cli 里，于是 cli
// 要 import gateway/special 并**直接读那个包的级注册表**（`special.Plugins()`）——
// 绕过 capability 去读别人的 owner 状态，正是 everything is module 要消灭的那种
// 依赖（同一条也适用于 `probe` / `metrics` / `schema-repair`）。搬回来之后 cli
// 不必认识 special 这个包，gateway 也能自己决定这些命令长什么样。
//
// 用法：
//
//	newgate st                 插件清单：一行一个，为什么存在
//	newgate st <插件>          单个插件的完整说明
//	newgate st on|off          整层开关
//	newgate st on|off <插件>   单独开关一个
//
// 为什么值得有这个命令：这一层会**改用户的请求**。用户排查「上游报错是不是
// newgate 改坏的」时，必须能一眼看到有哪些补丁在生效、并且能逐个关掉验证。
type specialCommand struct{}

var (
	_ cliapi.Command    = (*specialCommand)(nil)
	_ cliapi.Documented = (*specialCommand)(nil)
)

func (specialCommand) Names() []string {
	return []string{"st", "special", "special-treatment", "special_treatment"}
}

func (specialCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: "探测与观测",
		Usage:   "st [on|off] [插件]",
		Summary: "special_treatment 开关与说明",
	}
}

// Run 收到的 args **不含 "st" 这个动词**（见 cliapi.Command 的契约）。
func (specialCommand) Run(host cliapi.Host, args []string) int {
	st := store.LoadState()
	sub := cliapi.Arg(args, 0)
	name := cliapi.Arg(args, 1)

	if sub == "" || sub == "status" || sub == "ls" {
		return specialList(host, st)
	}
	// 直接给插件名 = 看它的完整说明（Why 常常是好几行，铺在清单里会淹掉）
	if p := findPlugin(sub); p != nil {
		return specialExplain(host, st, sub)
	}

	if sub != "on" && sub != "off" {
		return host.Die(64, fmt.Sprintf(
			"没有叫 %q 的插件（newgate st 看清单）；整层开关用 newgate st on|off", sub))
	}
	on := sub == "on"

	if name == "" {
		if err := store.SetSpecialTreatment(on); err != nil {
			return host.Die(70, err.Error())
		}
		if on {
			fmt.Println(style.Item(style.OK, "special_treatment 整层已开"))
		} else {
			fmt.Println(style.Item(style.Skip, "special_treatment 整层已关") +
				style.Dim("   请求原样转发，上游怪癖不再补"))
		}
		host.NotifyProxy()
		return 0
	}

	if findPlugin(name) == nil {
		return host.Die(65, fmt.Sprintf("没有叫 %q 的插件（newgate st 看清单）", name))
	}
	if err := store.SetSpecialPlugin(name, on); err != nil {
		return host.Die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, "插件 "+name+" 已开"))
	} else {
		fmt.Println(style.Item(style.Skip, "插件 "+name+" 已关"))
	}
	if !st.SpecialEnabled() {
		fmt.Println(style.Hint("整层仍处于关闭状态，需要先 newgate st on"))
	}
	host.NotifyProxy()
	return 0
}

func findPlugin(name string) special.Plugin {
	for _, p := range special.Plugins() {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// specialState 单个插件此刻的状态：层开关 + 单插件开关。
func specialState(st *domain.State, name string) (mark, word string) {
	switch {
	case !st.SpecialEnabled():
		return style.Skip, "整层关闭"
	case st.SpecialPluginOff(name):
		return style.Bad, "已单独关闭"
	}
	return style.OK, "生效"
}

func specialList(host cliapi.Host, st *domain.State) int {
	ps := special.Plugins()
	layer := style.Green("开")
	if !st.SpecialEnabled() {
		layer = style.Yellow("关（整层）")
	}
	fmt.Println(style.Title("newgate st", fmt.Sprintf("special_treatment %s · %d 个插件", layer, len(ps))))
	fmt.Println(style.Rule(72))
	if len(ps) == 0 {
		fmt.Println(style.Hint("没有注册任何插件"))
		return 0
	}
	t := style.NewTable("状态", "插件", "为什么存在")
	for _, p := range ps {
		mark, _ := specialState(st, p.Name())
		why := strings.SplitN(p.Why(), "\n", 2)[0]
		t.Row(style.Mark(mark), p.Name(), style.Dim(why))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint("上游怪癖补丁：只对认领本次请求的上游生效，改动逐条写日志"))
	fmt.Println(style.Hint("看完整说明 newgate st <插件> · 单独关 newgate st off <插件> · 整层关 newgate st off"))
	host.PrintThinkCache()
	return 0
}

func specialExplain(host cliapi.Host, st *domain.State, name string) int {
	p := findPlugin(name)
	mark, word := specialState(st, name)
	fmt.Println(style.Title("newgate st "+name, word))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(mark, p.Why()))
	fmt.Println()
	if st.SpecialPluginOff(name) {
		fmt.Println(style.Hint("打开：newgate st on " + name))
	} else {
		fmt.Println(style.Hint("关闭：newgate st off " + name))
	}
	return 0
}
