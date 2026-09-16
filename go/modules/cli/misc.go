package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/cli/tui"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
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