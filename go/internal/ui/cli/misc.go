package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/gateway/special"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/store"
	"github.com/rzbdz/newgate/go/internal/ui/style"
	"github.com/rzbdz/newgate/go/internal/ui/tui"
)

// cmdDebug turns on full request logging.
//
// It expires on its own by default: a single request can log 8KB or more
// (opencode's system prompt alone is ~97KB), so leaving it on indefinitely
// fills the disk.
func cmdDebug(args []string) int {
	on := len(args) > 1 && truthy(args[1])
	ttl := 30 * time.Minute
	if on {
		for _, a := range args {
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n > 0 {
				ttl = time.Duration(n) * time.Minute
			}
		}
		if has(args, "--forever") {
			ttl = 0
		}
	}
	if err := store.SetDebug(on, debugUntil(ttl)); err != nil {
		return die(70, err.Error())
	}
	if !on {
		fmt.Println(style.Item(style.Skip, "debug off"))
		return 0
	}
	if ttl > 0 {
		fmt.Println(style.Item(style.OK, fmt.Sprintf("debug on · %s 后自动关闭", prettyDur(int(ttl.Seconds())))))
		fmt.Println(style.Hint("不设期限：newgate debug on --forever"))
	} else {
		fmt.Println(style.Item(style.Warn, "debug on · 不自动关闭"))
		fmt.Println(style.Hint("记得 newgate debug off"))
	}
	fmt.Println(style.Hint("日志 " + paths.LogFile() + " · 16MB 轮转，保留 4 份"))
	notifyProxy()
	if daemon.Running() != nil {
		fmt.Println(style.Hint("即刻生效，无需重启"))
	}
	return 0
}

func debugUntil(ttl time.Duration) string {
	if ttl <= 0 {
		return ""
	}
	return time.Now().Add(ttl).Format(time.RFC3339)
}

func cmdSchemaRepair(on bool) int {
	if err := store.SetSchemaRepair(on); err != nil {
		return die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, "schema repair on") + style.Dim("   工具 schema 缺 required 时补一个空数组"))
	} else {
		fmt.Println(style.Item(style.Skip, "schema repair off") + style.Dim("   工具 schema 原样转发"))
	}
	notifyProxy()
	return 0
}

// cmdSpecial 管 special_treatment 插件层。
//
//	newgate st                 插件清单：一行一个，为什么存在
//	newgate st <插件>          单个插件的完整说明
//	newgate st on|off          整层开关
//	newgate st on|off <插件>   单独开关一个
//
// 为什么值得有这个命令：这一层会**改用户的请求**。用户排查「上游报错是不是
// newgate 改坏的」时，必须能一眼看到有哪些补丁在生效、并且能逐个关掉验证。
func cmdSpecial(args []string) int {
	st := store.LoadState()
	sub := arg(args, 1)
	name := arg(args, 2)

	if sub == "" || sub == "status" || sub == "ls" {
		return specialList(st)
	}
	// 直接给插件名 = 看它的完整说明（Why 常常是好几行，铺在清单里会淹掉）
	if p := findPlugin(sub); p != nil {
		return specialExplain(st, sub)
	}

	if sub != "on" && sub != "off" {
		return die(64, fmt.Sprintf("没有叫 %q 的插件（newgate st 看清单）；整层开关用 newgate st on|off", sub))
	}
	on := sub == "on"

	if name == "" {
		if err := store.SetSpecialTreatment(on); err != nil {
			return die(70, err.Error())
		}
		if on {
			fmt.Println(style.Item(style.OK, "special_treatment 整层已开"))
		} else {
			fmt.Println(style.Item(style.Skip, "special_treatment 整层已关") + style.Dim("   请求原样转发，上游怪癖不再补"))
		}
		notifyProxy()
		return 0
	}

	if findPlugin(name) == nil {
		return die(65, fmt.Sprintf("没有叫 %q 的插件（newgate st 看清单）", name))
	}
	if err := store.SetSpecialPlugin(name, on); err != nil {
		return die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, "插件 "+name+" 已开"))
	} else {
		fmt.Println(style.Item(style.Skip, "插件 "+name+" 已关"))
	}
	if !st.SpecialEnabled() {
		fmt.Println(style.Hint("整层仍处于关闭状态，需要先 newgate st on"))
	}
	notifyProxy()
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

func specialList(st *domain.State) int {
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
	printThinkCache()
	return 0
}

func specialExplain(st *domain.State, name string) int {
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

func cmdTUI() int {
	if err := tui.Run(); err != nil {
		return die(70, err.Error())
	}
	return 0
}

// printThinkCache 展示推理内容缓存的命中情况。
//
// 数字必须从**跑着的守护进程**取：缓存在守护进程的内存里，CLI 是另一个进程，
// 在这边读 thinkcache.Default 只会看到一个空缓存——那比不显示更误导人。
//
// 只报计数，永远不报内容。
func printThinkCache() {
	info, ps := proxyState()
	if info == nil || ps == nil {
		return
	}
	t := ps.Think
	fmt.Println()
	fmt.Println(style.Field("推理缓存", fmt.Sprintf("%d 条 · %s / %s · 命中 %d · 未命中 %d · 淘汰 %d",
		t.Entries, human(t.Bytes), human(t.MaxBytes), t.Hits, t.Misses, t.Evictions)))
	fmt.Println(style.Hint("客户端会丢弃上游的推理内容，代理代为保存并在下一轮原样补回"))
	if t.Misses > 0 {
		fmt.Println(style.Hint("未命中只能补空串（那几轮模型看不到自己的上一轮推理）；日志逐条有记"))
	}
	fmt.Println(style.Hint("内存 + thinkcache.bin 冷层；重启后自动装回，只存计数不存内容"))
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
