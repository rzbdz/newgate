package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"github.com/rzbdz/newgate/go/internal/store"
	"github.com/rzbdz/newgate/go/internal/ui/style"
)

// cmdOmo 管 omo（opencode 的 oh-my-openagent 插件）的 intra-agent 槽位。
//
// 槽位键（omo-sisyphus / cat-deep …）是**模块贡献的动态角色**：接管时由
// omo 模块注册给框架（runtime/injection/omo.go），框架把它们和档位一视同仁
// 地解析。所以这个命令只做三件事——看一眼现状、改缺省归属、解释解析结果。
//
// 用户不需要记键名：不带参数就是列表。
func cmdOmo(args []string) int {
	if len(args) == 0 {
		return omoList()
	}
	switch args[0] {
	case "ls", "list":
		return omoList()
	case "use", "set":
		if len(args) < 3 {
			return die(64, "用法：newgate omo use <槽位键> <@别的键|档位|provider/模型>")
		}
		return omoUse(args[1], args[2], true)
	case "unset", "reset":
		if len(args) < 2 {
			return die(64, "用法：newgate omo unset <槽位键>")
		}
		return omoUse(args[1], "", false)
	case "mode":
		if len(args) < 2 {
			return omoMode("")
		}
		return omoMode(args[1])
	case "explain":
		if len(args) < 2 {
			return die(64, "用法：newgate omo explain <槽位键>")
		}
		return omoExplain(args[1])
	}
	return die(64, "用法：newgate omo [ls | use <键> <归属> | unset <键> | mode current|suggested | explain <键>]")
}

func omoRegistry() *injection.OmoSlots {
	reg := injection.ReadOmoSlots()
	if reg == nil {
		fmt.Println(style.Item(style.Skip, "没有槽位登记表 "+paths.OmoSlotsFile()))
		fmt.Println(style.Hint("接管一次即生成：newgate on opencode"))
		os.Exit(0)
	}
	return reg
}

// omoList 槽位清单：一行一个键，六列答完「它现在是什么、建议是什么、实际用什么」。
//
// 建议的**理由**另起一段（塞进表格会把每行撑到折行）。docs/18 §10：用户唯一
// 会问的问题是「为什么不是我想的那个」。
func omoList() int {
	reg := omoRegistry()
	fmt.Println(style.Title("newgate omo",
		fmt.Sprintf("%d 个槽位键 · 模式 %s", len(reg.Slots), omoModeName(reg))))
	fmt.Println(style.Rule(78))
	fmt.Println(style.Hint("模式 current 按接管时的现状，suggested 按建议 · 切换 newgate omo mode <模式>"))

	t := style.NewTable("键", "槽位", "接管前", "现状", "建议", "生效")
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
		t.Row(s.Key, style.Dim(s.Kind+"/"+s.Name), style.Dim(was), s.Current, sug, eff)
	}
	fmt.Println()
	fmt.Print(t.String())
	if len(reg.Overrides) > 0 {
		fmt.Println(style.Hint("* 有覆盖（newgate omo use 写入），优先级最高"))
	}

	var diff []injection.OmoSlot
	for _, s := range reg.Slots {
		if s.Suggested != "" && s.Suggested != s.Current {
			diff = append(diff, s)
		}
	}
	if len(diff) > 0 {
		fmt.Print(style.Section(fmt.Sprintf("建议与现状不同（%d 个）", len(diff))) +
			style.Dim("   newgate omo mode suggested 全部采纳") + "\n")
		d := style.NewTable("键", "现状", "建议", "依据")
		for _, s := range diff {
			d.Row(s.Key, s.Current, style.Yellow(s.Suggested), style.Dim(s.Why))
		}
		fmt.Print(d.String())
	}
	fmt.Println()
	fmt.Println(style.Hint("profile 里直接写键名同样有效：omo-sisyphus=@normal, terra/medium"))
	return 0
}

func omoModeName(reg *injection.OmoSlots) string {
	if reg.Mode == "suggested" {
		return "suggested"
	}
	return "current"
}

// omoDiffCount 建议档位与现状不同的键数（有覆盖的不算——那些已经定了）。
func omoDiffCount(reg *injection.OmoSlots) int {
	if reg == nil {
		return 0
	}
	n := 0
	for _, s := range reg.Slots {
		if _, over := reg.Overrides[s.Key]; over {
			continue
		}
		if s.Suggested != "" && s.Suggested != s.Current {
			n++
		}
	}
	return n
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// omoUse 写/删一个覆盖。value 为空表示删除。
func omoUse(key, value string, set bool) int {
	reg := injection.ReadOmoSlots()
	if reg == nil {
		return die(65, "没有 omo 槽位登记表；先 newgate on opencode")
	}
	if _, ok := reg.SlotOf(key); !ok {
		return die(64, fmt.Sprintf("没有这个槽位键 %q（newgate omo 看列表）", key))
	}
	if set {
		if err := validateBindingValue(value); err != nil {
			return die(64, err.Error())
		}
		if reg.Overrides == nil {
			reg.Overrides = map[string]string{}
		}
		reg.Overrides[key] = value
	} else {
		delete(reg.Overrides, key)
	}
	if err := injection.WriteOmoSlots(reg); err != nil {
		return die(70, "写注册表失败: "+err.Error())
	}
	if set {
		fmt.Println(style.Item(style.OK, key+" 缺省归属 → "+style.Cyan(value)))
	} else {
		fmt.Println(style.Item(style.OK, key+" 覆盖已删除，回到 "+omoModeName(reg)))
	}
	notifyProxy()
	fmt.Println(style.Hint("1 秒内自动生效；profile 里显式写了这个键则以 profile 为准"))
	return 0
}

func validateBindingValue(v string) error {
	if strings.HasPrefix(v, "@") {
		key := strings.TrimPrefix(v, "@")
		if key == "" || strings.ContainsAny(key, "/ ") {
			return fmt.Errorf("引用写成 @键名（如 @normal），得到 %q", v)
		}
		return nil
	}
	if strings.Contains(v, "/") {
		if _, err := domain.ParseBindingString(v); err != nil {
			return err
		}
		return nil
	}
	if !domain.IsKnownRole(v) {
		return fmt.Errorf("%q 既不是档位（heavy/normal/mid/light/vision）、"+
			"也不是已注册的动态角色键、也不是 provider/模型", v)
	}
	return nil
}

func omoMode(mode string) int {
	reg := omoRegistry()
	if mode == "" {
		fmt.Println(style.Title("newgate omo mode", omoModeName(reg)))
		fmt.Println(style.Rule(64))
		t := style.NewTable("模式", "含义")
		t.Row("current", style.Dim("每个键按接管时的现状（默认，行为不变）"))
		t.Row("suggested", style.Dim("每个键按建议，由模型体格与 variant 强度推出"))
		fmt.Print(t.String())
		fmt.Println(style.Hint("切换：newgate omo mode current|suggested"))
		return 0
	}
	switch mode {
	case "current", "suggested":
	default:
		return die(64, "模式只认 current / suggested")
	}
	reg.Mode = mode
	if err := injection.WriteOmoSlots(reg); err != nil {
		return die(70, "写注册表失败: "+err.Error())
	}
	fmt.Println(style.Item(style.OK, "模式 → "+style.Cyan(mode)))
	n := 0
	for _, s := range reg.Slots {
		if _, ok := reg.Overrides[s.Key]; ok {
			continue
		}
		if s.Suggested != "" && s.Suggested != s.Current {
			n++
		}
	}
	if mode == "suggested" {
		fmt.Println(style.Hint(fmt.Sprintf("%d 个键的归属随之改变（有覆盖的不受影响）；不满意：newgate omo mode current", n)))
	} else {
		fmt.Println(style.Hint("已回到接管时的现状"))
	}
	notifyProxy()
	return 0
}

// omoExplain 把一个槽位键解析成实际的 fallback 链——「为什么不是我想的那个」
// 只能靠这个回答（docs/18 §10）。
func omoExplain(key string) int {
	reg := injection.ReadOmoSlots()
	if reg == nil {
		return die(65, "没有 omo 槽位登记表；先 newgate on opencode")
	}
	// 先装快照：角色键是 store.Load 里跟着刷新的（core/roleprov），
	// 不先装一次的话 IsKnownRole 会说不认识我们自己刚注册的键。
	snap, err := store.Load()
	if err != nil {
		return die(65, err.Error())
	}
	if !domain.IsKnownRole(key) {
		return die(64, fmt.Sprintf("%q 不是已知角色键（newgate omo ls 看清单）", key))
	}
	st := snap.State
	heads := map[string][]string{st.DefaultProfile: {"默认"}}
	for agent, p := range st.Active {
		heads[p] = append(heads[p], agent)
	}
	names := make([]string, 0, len(heads))
	for h := range heads {
		names = append(names, h)
	}
	sort.Strings(names)

	sub := "模块动态角色，非 omo 槽位"
	if sl, ok := reg.SlotOf(key); ok {
		sub = sl.Kind + "/" + sl.Name
	}
	fmt.Println(style.Title("newgate omo "+key, sub))
	fmt.Println(style.Rule(64))

	if sl, ok := reg.SlotOf(key); ok {
		t := style.NewTable("字段", "值")
		t.Row("接管前", style.Dim(dash(sl.Was)+" "+dash(sl.Variant)))
		t.Row("现状", sl.Current)
		t.Row("建议", dash(sl.Suggested))
		t.Row("生效", style.Cyan(reg.SlotBinding(key)))
		fmt.Print(t.String())
		if sl.Why != "" {
			fmt.Println(style.Hint("建议依据 " + sl.Why))
		}
	}

	_, live := proxyState()
	available := availableFromProxy(live)
	rank := rankFromProxy(live)
	for _, head := range names {
		fmt.Print(style.Section("链头 "+head) + style.Dim("   "+strings.Join(heads[head], ", ")) + "\n")
		steps, skips := resolve.BuildChain(key, snap.Profiles, snap.Providers, resolve.Opts{
			Active:    head,
			Available: available,
			Rank:      rank,
			MaxSteps:  st.Chain.Attempts(),
		})
		if len(steps) == 0 {
			fmt.Println(style.Item(style.Bad, "无可用候选"))
		} else {
			t := style.NewTable("#", "profile", "绑定")
			t.AlignRight(0)
			for i, s := range steps {
				t.Row(fmt.Sprintf("%d", i+1), s.Profile, s.Binding.String())
			}
			fmt.Print(t.String())
		}
		if len(skips) > 0 {
			printSkips(skips)
		}
	}
	return 0
}
