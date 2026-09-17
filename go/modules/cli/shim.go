package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rzbdz/newgate/go/modules/cli/style"

	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
	"github.com/rzbdz/newgate/go/modules/runtime/injection"
	"github.com/rzbdz/newgate/go/modules/runtime/takeover"
)

// cmdShim 是**底层逃生口**，不是日常命令。
//
// 日常接管请用 newgate on/off <agent>：用户不该被迫知道自己的工具是靠
// PATH shim 接管还是靠改配置文件接管——那是 runtime/takeover 的事
// （docs/08-operations.md：逃生口是一等功能，但它得摆在逃生口的位置上）。
//
// 这里只留三件 takeover 层不做的事：
//   - status：把 shim 目录的真实情况摊开，排查「装了却没生效」
//   - uninstall：连 rc 里那行 PATH 一起删干净（takeover 只摘链接，
//     留着空目录在 PATH 里是无害的，也让下次 on 不用再动 rc）
//   - install/off：老命令的别名，直接转给 on/off，免得肌肉记忆报错
func cmdShim(agents agentapi.AgentCatalog, sub, which string) int {
	switch sub {
	case "install", "add", "on":
		fmt.Println(style.Hint("直接用 newgate on " + which + " 即可，机制由 newgate 自行选择"))
		fmt.Println()
		return cmdTakeover(agents, which)

	case "off", "remove", "rm":
		fmt.Println(style.Hint("直接用 newgate off " + which + " 即可"))
		fmt.Println()
		return cmdRelease(which)

	case "uninstall", "purge":
		for _, n := range injection.Installed() {
			if err := injection.Uninstall(n); err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, n+": "+err.Error()))
				continue
			}
			fmt.Println(style.Item(style.OK, "摘掉 shim "+n))
		}
		for _, rc := range injection.RCFiles() {
			changed, err := injection.RemoveFromRC(rc)
			if err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, rc+": "+err.Error()))
				continue
			}
			if changed {
				fmt.Println(style.Item(style.OK, "从 "+rc+" 删掉 PATH 行"))
			}
		}
		if fg := injection.Foreign(); len(fg) > 0 {
			// 只在我们**认得这个 agent 名**时才出声：那种情况是外来同名文件
			// 会遮住我们的 shim，值得看一眼；目录里躺着 claude-bak 这种
			// 无关文件是用户自己的事，报出来只是噪音。
			var shadow []string
			for _, n := range fg {
				if _, known := agents.Get(n); known {
					shadow = append(shadow, n)
				}
			}
			if len(shadow) > 0 {
				fmt.Println(style.Hint("同名外部文件仍在 " + injection.Dir() + "：" + strings.Join(shadow, ", ") + "（会遮住 newgate 的 shim）"))
			}
		}
		fmt.Println()
		fmt.Println(style.Hint("PATH 痕迹已清空，重开 shell 生效"))
		fmt.Println(style.Hint("这不改接管意愿：下次 newgate start 会重新装回来"))
		return 0

	default:
		return shimStatus(agents)
	}
}

// shimStatus 摊开 shim 目录的真实情况，用于排查「装了却没生效」。
func shimStatus(agents agentapi.AgentCatalog) int {
	fmt.Println(style.Title("newgate shim", injection.Dir()))
	fmt.Println(style.Rule(72))

	if injection.InPath() {
		fmt.Println(style.Field("在 PATH", style.Green("是")))
	} else {
		fmt.Println(style.Field("在 PATH", style.Yellow("否")+style.Dim("   重开 shell 或 exec $SHELL -l")))
	}

	inst := injection.Installed()
	if len(inst) == 0 {
		fmt.Println(style.Field("已装", style.Dim("无")))
	} else {
		t := style.NewTable("shim", "指向")
		for _, n := range inst {
			a, ok := agents.Get(n)
			if !ok {
				t.Row(n, style.Dim("未知 agent"))
				continue
			}
			if real, err := a.FindReal(injection.Dir()); err == nil {
				t.Row(n, real)
			} else {
				t.Row(n, style.Red("找不到真实可执行文件（转发会失败）"))
			}
		}
		fmt.Println(style.Field("已装", fmt.Sprintf("%d 个", len(inst))))
		fmt.Print(t.String())
	}

	t := style.NewTable("rc 文件", "PATH 行")
	for _, rc := range injection.RCFiles() {
		mark := style.Dim("无")
		if injection.HasBlock(rc) {
			mark = style.Green("有")
		}
		t.Row(rc, mark)
	}
	fmt.Print(t.String())

	// 期望态 vs 现实态：只列走 shim 机制的 agent，其余与 shim 无关。
	var rows [][2]string
	for _, s := range takeover.List() {
		if s.Mechanism != takeover.MechShim {
			continue
		}
		state := style.Mark(style.OK) + " 生效"
		switch {
		case s.Wanted && !s.Active:
			state = style.Mark(style.Bad) + " 声明接管但未生效"
		case !s.Wanted && s.Active:
			state = style.Mark(style.Warn) + " 未声明接管却仍装着"
		case !s.Wanted:
			state = style.Mark(style.Skip) + " 未接管"
		}
		rows = append(rows, [2]string{s.Agent, state})
	}
	if len(rows) > 0 {
		fmt.Print(style.Section("接管意愿") + "\n")
		t := style.NewTable("agent", "状态")
		for _, r := range rows {
			t.Row(r[0], r[1])
		}
		fmt.Print(t.String())
	}

	fmt.Println()
	fmt.Println(style.Hint("底层逃生口。日常：newgate on|off <agent> · newgate shim uninstall 连 rc 一起清"))
	return 0
}
