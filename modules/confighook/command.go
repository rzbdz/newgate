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
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/store"
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
		Usage: "agents", Summary: i18n.T("known agents and their model slots", nil)}
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

	fmt.Println(style.Title("newgate agents", i18n.T("{n} total", i18n.A{"n": len(names)})))
	fmt.Println(style.Rule(72))

	for _, n := range names {
		a, _ := agents.Get(n)
		profile := st.ActiveFor(a.ID)
		if profile == "" {
			profile = st.DefaultProfile
		}
		fmt.Println()
		fmt.Println("  " + style.Bold(a.ID) +
			style.Dim("   "+i18n.T("{dialect} dialect", i18n.A{"dialect": a.Dialect})) +
			style.Dim("   profile ") + style.Cyan(profile))
		if a.Notes != "" {
			fmt.Println(style.Hint(a.Notes))
		}
		if len(a.Slots) == 0 {
			fmt.Println(style.Hint(i18n.T("slots are discovered from the config file at startup", nil)))
			continue
		}
		t := style.NewTable(i18n.T("Slot", nil), i18n.T("Tier", nil), i18n.T("Env var", nil))
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
	fmt.Println(style.Hint(i18n.T("switch a single agent only: newgate --set-profile <name> --agent <agent>", nil)))
	return 0
}

// glossary 贡献帮助屏术语表里的「agent」一行。
//
// 它是**客户端目录**的词汇（哪些 CLI 被接管），所以归本模块——界面不必认识
// AgentCatalog 才写得出来（见 cliapi.Glossarist）。
type glossary struct{ agents AgentCatalog }

var _ cliapi.Glossarist = glossary{}

func (g glossary) Glossary() []cliapi.GlossaryLine {
	names := g.agents.Names()
	list := i18n.T("(not assembled)", nil)
	switch {
	case len(names) == 0:
		list = i18n.T("(none)", nil)
	default:
		list = strings.Join(names, " / ")
	}
	return []cliapi.GlossaryLine{{Rank: 10, Term: "agent",
		Definition: i18n.T("CLIs taken over: {list}", i18n.A{"list": list})}}
}
