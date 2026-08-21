package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"github.com/rzbdz/newgate/go/internal/runtime/takeover"
)

// cmdShim 是**底层逃生口**，不是日常命令。
//
// 日常接管请用 newgate on/off <agent>：用户不该被迫知道自己的工具是靠
// PATH shim 接管还是靠改配置文件接管——那是 runtime/takeover 的事
// （docs/16 §6.3：逃生口是一等功能，但它得摆在逃生口的位置上）。
//
// 这里只留三件 takeover 层不做的事：
//   - status：把 shim 目录的真实情况摊开，排查「装了却没生效」
//   - uninstall：连 rc 里那行 PATH 一起删干净（takeover 只摘链接，
//     留着空目录在 PATH 里是无害的，也让下次 on 不用再动 rc）
//   - install/off：老命令的别名，直接转给 on/off，免得肌肉记忆报错
func cmdShim(sub, which string) int {
	switch sub {
	case "install", "add":
		fmt.Printf("提示：现在直接用 `newgate on %s` 就行，机制我们自己挑。\n\n", which)
		return cmdTakeover(which)

	case "off", "remove", "rm":
		fmt.Printf("提示：现在直接用 `newgate off %s` 就行。\n\n", which)
		return cmdRelease(which)

	case "on":
		fmt.Printf("提示：现在直接用 `newgate on %s` 就行。\n\n", which)
		return cmdTakeover(which)

	case "uninstall", "purge":
		for _, n := range injection.Installed() {
			if err := injection.Uninstall(n); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ %s: %v\n", n, err)
				continue
			}
			fmt.Printf("✓ 摘掉 shim %s\n", n)
		}
		for _, rc := range injection.RCFiles() {
			changed, err := injection.RemoveFromRC(rc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ %s: %v\n", rc, err)
				continue
			}
			if changed {
				fmt.Printf("✓ 从 %s 删掉 PATH 那行\n", rc)
			}
		}
		if fg := injection.Foreign(); len(fg) > 0 {
			fmt.Printf("\n没动 %s 里这些不是我们放的东西：%s\n",
				injection.Dir(), strings.Join(fg, ", "))
		}
		fmt.Println("\nPATH 痕迹已清干净。重开 shell 生效。")
		fmt.Println("  注意：这不改接管意愿。下次 newgate start 会重新装回来。")
		return 0

	default:
		fmt.Printf("shim 目录     %s\n", injection.Dir())
		if injection.InPath() {
			fmt.Printf("在 PATH 里    是\n")
		} else {
			fmt.Printf("在 PATH 里    否 —— 装了也不会生效，重开 shell 或 `exec $SHELL -l`\n")
		}

		inst := injection.Installed()
		if len(inst) == 0 {
			fmt.Printf("已装 shim     （无）\n")
		} else {
			fmt.Printf("已装 shim     %s\n", strings.Join(inst, ", "))
		}
		for _, n := range inst {
			a, ok := agents.Get(n)
			if !ok {
				continue
			}
			if real, err := a.FindReal(injection.Dir()); err == nil {
				fmt.Printf("              %s → 真实 %s\n", n, real)
			} else {
				fmt.Printf("              %s → ⚠ 找不到真实可执行文件（shim 会转发失败）: %v\n", n, err)
			}
		}
		if fg := injection.Foreign(); len(fg) > 0 {
			fmt.Printf("非我方条目    %s（我们不碰，也不算接管）\n", strings.Join(fg, ", "))
		}
		for _, rc := range injection.RCFiles() {
			mark := "无"
			if injection.HasBlock(rc) {
				mark = "有"
			}
			fmt.Printf("rc PATH 行    %-36s %s\n", rc, mark)
		}

		// 期望态 vs 现实态：只列走 shim 机制的 agent，其余与 shim 无关。
		var rows []string
		for _, s := range takeover.List() {
			if s.Mechanism != takeover.MechShim {
				continue
			}
			rows = append(rows, fmt.Sprintf("%s 想接管=%v 实际=%v", s.Agent, s.Wanted, s.Active))
		}
		if len(rows) > 0 {
			fmt.Printf("接管意愿      %s\n", strings.Join(rows, "  "))
		}

		fmt.Println("\n这是底层逃生口。日常请用：")
		fmt.Println("  newgate on <agent> / newgate off <agent>    接管 / 放开一个 agent")
		fmt.Println("  newgate shim uninstall                     连 rc 里的 PATH 行一起清干净")
		return 0
	}
}
