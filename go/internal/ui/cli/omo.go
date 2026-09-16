package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/gateway/health"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"github.com/rzbdz/newgate/go/internal/store"
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
		fmt.Printf("还没有 omo 槽位登记表（%s）。\n", paths.OmoSlotsFile())
		fmt.Println("接管一次就有了：newgate on opencode")
		os.Exit(0)
	}
	return reg
}

func omoList() int {
	reg := omoRegistry()
	fmt.Printf("\033[1momo 槽位\033[0m  %d 个键   模式=%s   注册表=%s\n",
		len(reg.Slots), omoModeName(reg), paths.OmoSlotsFile())
	fmt.Printf("  模式 current=按接管时的现状，suggested=按建议（改：newgate omo mode suggested）\n\n")
	fmt.Printf("  %-22s %-10s %-26s %-6s %-9s %-9s %s\n",
		"键", "槽位", "接管前的模型", "强度", "现状", "建议", "生效")
	for _, s := range reg.Slots {
		eff := reg.SlotBinding(s.Key)
		over := ""
		if _, ok := reg.Overrides[s.Key]; ok {
			over = " *"
		}
		mark := ""
		if s.Suggested != "" && s.Suggested != s.Current {
			mark = " \033[33m←\033[0m"
		}
		fmt.Printf("  %-22s %-10s %-26s %-6s %-9s %s%-8s %s\n",
			s.Key, s.Kind+"/"+s.Name, dash(s.Was), dash(s.Variant),
			s.Current, mark, dash(s.Suggested), eff+over)
	}
	if len(reg.Overrides) > 0 {
		fmt.Println("\n  * = 有覆盖（newgate omo use 写的），优先级最高")
	}
	// 建议的**理由**单独列在下面：塞进表格会把每行撑到折行，反而看不清
	// （docs/18 §10：唯一会被问的问题是「为什么不是我想的那个」）。
	var diff []injection.OmoSlot
	for _, s := range reg.Slots {
		if s.Suggested != "" && s.Suggested != s.Current {
			diff = append(diff, s)
		}
	}
	if len(diff) > 0 {
		fmt.Printf("\n  \033[1m建议与现状不同的 %d 个键\033[0m（newgate omo mode suggested 全部照建议来）\n", len(diff))
		for _, s := range diff {
			fmt.Printf("    %-22s %-8s → %-8s \033[2m%s\033[0m\n",
				s.Key, s.Current, s.Suggested, s.Why)
		}
	}
	fmt.Println("\n  profile 里直接写这个键也能改归属，例如：omo-sisyphus=@normal, terra/medium")
	return 0
}

func omoModeName(reg *injection.OmoSlots) string {
	if reg.Mode == "suggested" {
		return "suggested"
	}
	return "current"
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
		fmt.Printf("✓ %s 的缺省归属 → %s\n", key, value)
	} else {
		fmt.Printf("✓ %s 的覆盖已删除，回到%s\n", key, omoModeName(reg))
	}
	notifyProxy()
	fmt.Println("  （daemon 1 秒内自动生效；profile 里显式写了这个键的话，以 profile 为准）")
	return 0
}

func validateBindingValue(v string) error {
	if strings.HasPrefix(v, "@") {
		key := strings.TrimPrefix(v, "@")
		if key == "" || strings.ContainsAny(key, "/ ") {
			return fmt.Errorf("引用要写成 @键名（如 @normal），得到 %q", v)
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
		return fmt.Errorf("%q 既不是档位名（heavy/normal/mid/light/vision）、"+
			"也不是已注册的动态角色键，也不是 provider/模型", v)
	}
	return nil
}

func omoMode(mode string) int {
	reg := omoRegistry()
	if mode == "" {
		fmt.Printf("当前模式：%s\n", omoModeName(reg))
		fmt.Println("  current   每个键按接管时的现状（不改行为，默认）")
		fmt.Println("  suggested 每个键按建议（模型体格 + variant 强度算出来的）")
		fmt.Println("用法：newgate omo mode current|suggested")
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
	fmt.Printf("✓ 模式 → %s\n", mode)
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
		fmt.Printf("  %d 个键的归属跟着变（有覆盖的键不受影响）。看完不满意：newgate omo mode current\n", n)
	} else {
		fmt.Println("  回到接管时的现状。")
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
		return die(64, fmt.Sprintf("%q 不是已知角色键（newgate omo 看列表）", key))
	}
	st := snap.State
	heads := map[string][]string{st.DefaultProfile: {"(默认)"}}
	for agent, p := range st.Active {
		heads[p] = append(heads[p], agent)
	}
	names := make([]string, 0, len(heads))
	for h := range heads {
		names = append(names, h)
	}
	sort.Strings(names)

	if sl, ok := reg.SlotOf(key); ok {
		fmt.Printf("\033[1m%s\033[0m  %s/%s  接管前=%s variant=%s\n",
			key, sl.Kind, sl.Name, dash(sl.Was), dash(sl.Variant))
		fmt.Printf("  现状=%s  建议=%s  生效归属=%s", sl.Current, dash(sl.Suggested), reg.SlotBinding(key))
		if sl.Why != "" {
			fmt.Printf("  （%s）", sl.Why)
		}
		fmt.Println()
	} else {
		fmt.Printf("\033[1m%s\033[0m  （模块动态角色，不是 omo 槽位）\n", key)
	}

	for _, head := range names {
		fmt.Printf("\n  链头 %s (用于: %s)\n", head, strings.Join(heads[head], ", "))
		steps, skips := resolve.BuildChain(key, snap.Profiles, snap.Providers, resolve.Opts{
			Active:    head,
			Available: health.Default.Available,
			MaxSteps:  st.Chain.Attempts(),
		})
		for i, s := range steps {
			mark := "   "
			if i == 0 {
				mark = " → "
			}
			fmt.Printf("   %s%d. %-14s %s\n", mark, i+1, s.Profile, s.Binding)
		}
		for _, sk := range skips {
			t := sk.Target
			if t == "" {
				t = "(整个 profile)"
			}
			fmt.Printf("     -  %-14s %-30s \033[2m%s\033[0m\n", sk.Profile, t, sk.Reason)
		}
		if len(steps) == 0 {
			fmt.Println("     \033[31m没有可用候选\033[0m")
		}
	}
	return 0
}
