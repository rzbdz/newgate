package opencodeomo

import (
	"fmt"
	"os"
	"sort"
	"strings"

	cliapi "github.com/rzbdz/newgate/go/modules/cli/api"
	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"
)

type omoCommand struct{}

var _ cliapi.Command = (*omoCommand)(nil)

func (omoCommand) Names() []string { return []string{"omo", "slots"} }

func (omoCommand) Run(host cliapi.Host, args []string) int {
	return cmdOmo(host, args)
}

// cmdOmo 管 omo（opencode 的 oh-my-openagent 插件）的 intra-agent 槽位。
//
// 槽位键（omo-sisyphus / cat-deep …）是**模块贡献的动态角色**：接管时由
// omo 模块注册给框架（runtime/injection/omo.go），框架把它们和档位一视同仁
// 地解析。所以这个命令只做三件事——看一眼现状、改缺省归属、解释解析结果。
//
// 用户不需要记键名：不带参数就是列表。
func cmdOmo(host cliapi.Host, args []string) int {
	if len(args) == 0 {
		return omoList()
	}
	switch args[0] {
	case "ls", "list":
		return omoList()
	case "use", "set":
		if len(args) < 3 {
			return host.Die(64, "用法：newgate omo use <槽位键> <@别的键|档位|provider/模型>")
		}
		return omoUse(host, args[1], args[2], true)
	case "unset", "reset":
		if len(args) < 2 {
			return host.Die(64, "用法：newgate omo unset <槽位键>")
		}
		return omoUse(host, args[1], "", false)
	case "mode":
		if len(args) < 2 {
			return omoMode(host, "")
		}
		return omoMode(host, args[1])
	case "explain":
		if len(args) < 2 {
			return host.Die(64, "用法：newgate omo explain <槽位键>")
		}
		return omoExplain(host, args[1])
	}
	return host.Die(64, "用法：newgate omo [ls | use <键> <归属> | unset <键> | mode current|suggested | explain <键>]")
}

func omoRegistry() *OmoSlots {
	reg := ReadOmoSlots()
	if reg == nil {
		fmt.Println(style.Item(style.Skip, "没有槽位登记表 "+SlotsFile()))
		fmt.Println(style.Hint("接管一次即生成：newgate on opencode"))
		os.Exit(0)
	}
	return reg
}

// omoList 槽位清单使用纵向卡片。键、槽位和模型标识符都是不可分割的信息，
// 不能为了六列表格从中间硬切；属性放在后续缩进行。
func omoList() int {
	reg := omoRegistry()
	fmt.Println(style.Title("newgate omo",
		fmt.Sprintf("%d 个槽位键 · 模式 %s", len(reg.Slots), omoModeName(reg))))
	fmt.Println(style.Rule(78))
	fmt.Println(style.Hint("current=接管现状 · suggested=建议 · 切换：newgate omo mode <模式>"))

	fmt.Println()
	for _, s := range reg.Slots {
		eff := reg.SlotBinding(s.Key)
		if _, ok := reg.Overrides[s.Key]; ok {
			eff = style.Cyan(eff) + style.Dim("*")
		}
		was := dash(s.Was)
		if s.Variant != "" {
			was += style.Dim("(" + s.Variant + ")")
		}
		sug := style.Dim(dash(s.Suggested))
		if s.Suggested != "" && s.Suggested != s.Current {
			sug = style.Yellow(s.Suggested)
		}
		fmt.Print(omoSlotCard(s.Key, s.Kind+"/"+s.Name, was, s.Current, sug, eff))
	}
	if len(reg.Overrides) > 0 {
		fmt.Println(style.Hint("* 有覆盖（newgate omo use 写入），优先级最高"))
	}

	var diff []OmoSlot
	for _, s := range reg.Slots {
		if s.Suggested != "" && s.Suggested != s.Current {
			diff = append(diff, s)
		}
	}
	if len(diff) > 0 {
		fmt.Print(style.Section(fmt.Sprintf("建议与现状不同（%d 个）", len(diff))) +
			style.Dim("   newgate omo mode suggested 全部采纳") + "\n")
		for _, s := range diff {
			fmt.Println(style.Item(style.Skip, s.Key))
			line := s.Current + " → " + style.Yellow(s.Suggested)
			if s.Why != "" {
				line += " · " + style.Dim(s.Why)
			}
			fmt.Println(style.Hint(line))
		}
	}
	fmt.Println()
	fmt.Println(style.Hint("profile 里直接写键名同样有效：omo-sisyphus=@normal, terra/medium"))
	return 0
}