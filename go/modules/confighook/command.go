package confighook

// 本文件是 `newgate agents`：列出已知客户端及其模型槽位。
//
// **为什么它住在这里**（2026-09-18）：agent 是谁、有哪些槽位、槽位走哪个环境变量，
// 全是**客户端描述符**的知识，而描述符注册表归本模块（AgentCatalog）。它以前写在
// modules/cli/profile.go 里，界面因此要认识 AgentCatalog 的每个字段。
//
// 依赖方向：config-hook → cli/extension（叶子契约），不是 → modules/cli。

import (
	"fmt"
	"sort"

	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/store"
)

// agentsCommand 是 `newgate agents`。
type agentsCommand struct{ agents AgentCatalog }

var (
	_ cliapi.Command    = (*agentsCommand)(nil)
	_ cliapi.Documented = (*agentsCommand)(nil)
)

func (agentsCommand) Names() []string { return []string{"agents"} }

func (agentsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "agents", Summary: "已知 agent 及其模型槽位"}
}

func (c agentsCommand) Run(_ cliapi.Host, _ []string) int { return runAgents(c.agents) }

// cmdAgents 已知 agent 及其模型槽位。
//
// 每个 agent 一个小节：头一行是身份（方言 + 当前 profile），槽位一张表。
// 以前是一张通铺大表，说明列又长，中文一撑就错位。
func runAgents(agents AgentCatalog) int {
	st := store.LoadState()
	names := agents.Names()
	sort.Strings(names)

	fmt.Println(style.Title("newgate agents", fmt.Sprintf("%d 个", len(names))))
	fmt.Println(style.Rule(72))

	for _, n := range names {
		a, _ := agents.Get(n)
		profile := st.ActiveFor(a.ID)
		if profile == "" {
			profile = st.DefaultProfile
		}
		fmt.Println()
		fmt.Println("  " + style.Bold(a.ID) +
			style.Dim("   "+a.Dialect+" 方言") +
			style.Dim("   profile ") + style.Cyan(profile))
		if a.Notes != "" {
			fmt.Println(style.Hint(a.Notes))
		}
		if len(a.Slots) == 0 {
			fmt.Println(style.Hint("槽位在启动时从配置文件发现"))
			continue
		}
		t := style.NewTable("槽位", "档位", "环境变量")
		for _, s := range a.Slots {
			t.Row(s.Name, style.Cyan(s.Tier), s.EnvVar)
		}
		fmt.Print(t.String())
		// 说明单独一行：塞进表格会把整张表撑到一百多列，反而没法对读。
		for _, s := range a.Slots {
			if s.Desc != "" {
				fmt.Println(style.Hint(s.Name + "  " + s.Desc))
			}
		}
	}

	fmt.Println()
	fmt.Println(style.Hint("只切单个 agent：newgate --set-profile <名> --agent <agent>"))
	return 0
}
