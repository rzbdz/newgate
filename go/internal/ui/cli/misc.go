package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/internal/gateway/special"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/store"
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
		fmt.Println("✓ debug off")
		return 0
	}
	if ttl > 0 {
		fmt.Printf("✓ debug on, auto-off in %v (guards against filling the disk)\n", ttl)
		fmt.Println("  keep it on indefinitely: newgate debug on --forever")
	} else {
		fmt.Println("✓ debug on, no expiry — remember to run `newgate debug off`")
	}
	fmt.Printf("  log %s, rotating at 16MB keeping 4 files\n", paths.LogFile())
	notifyProxy()
	if daemon.Running() != nil {
		fmt.Println("  effective immediately; no restart needed")
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
		fmt.Println(`✓ schema repair on (adds "required": [] where a tool schema omits it)`)
	} else {
		fmt.Println("✓ schema repair off — tools are forwarded exactly as received")
	}
	notifyProxy()
	return 0
}

// cmdSpecial 管 special_treatment 插件层。
//
//	newgate st                 列出插件、各自为什么存在、开着还是关着
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
		state := "开"
		if !st.SpecialEnabled() {
			state = "关（整层）"
		}
		fmt.Printf("special_treatment  %s\n", state)
		fmt.Println("  上游怪癖补丁：只对认领了这次请求的上游生效，改动逐条写日志。")
		ps := special.Plugins()
		if len(ps) == 0 {
			fmt.Println("\n（没有注册任何插件）")
			return 0
		}
		fmt.Println()
		for _, p := range ps {
			mark := "✓"
			if !st.SpecialEnabled() {
				mark = "·"
			} else if st.SpecialPluginOff(p.Name()) {
				mark = "✗"
			}
			// Why 可以多行：第一行跟在名字后面，其余行对齐续行。
			for i, line := range strings.Split(p.Why(), "\n") {
				if i == 0 {
					fmt.Printf("  %s %-10s %s\n", mark, p.Name(), line)
					continue
				}
				fmt.Printf("  %s %-10s %s\n", " ", "", line)
			}
		}
		fmt.Println("\n  ✓ 生效   ✗ 已单独关掉   · 整层关着")
		fmt.Println("  单独关一个: newgate st off <插件>       整层关: newgate st off")
		return 0
	}

	if sub != "on" && sub != "off" {
		return die(64, "用法: newgate st [on|off] [插件名]")
	}
	on := sub == "on"

	if name == "" {
		if err := store.SetSpecialTreatment(on); err != nil {
			return die(70, err.Error())
		}
		if on {
			fmt.Println("✓ special_treatment 整层已开")
		} else {
			fmt.Println("✓ special_treatment 整层已关 —— 请求原样转发，上游怪癖不再被补")
		}
		notifyProxy()
		return 0
	}

	known := false
	for _, p := range special.Plugins() {
		if p.Name() == name {
			known = true
		}
	}
	if !known {
		return die(65, fmt.Sprintf("没有叫 %q 的插件（newgate st 看列表）", name))
	}
	if err := store.SetSpecialPlugin(name, on); err != nil {
		return die(70, err.Error())
	}
	if on {
		fmt.Printf("✓ 插件 %s 已开\n", name)
	} else {
		fmt.Printf("✓ 插件 %s 已关\n", name)
	}
	if !st.SpecialEnabled() {
		fmt.Println("  注意：整层还是关着的，得先 `newgate st on`")
	}
	notifyProxy()
	return 0
}

func cmdTUI() int {
	if err := tui.Run(); err != nil {
		return die(70, err.Error())
	}
	return 0
}
