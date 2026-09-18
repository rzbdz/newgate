// Package cli 是命令行前端。
//
// 分层规则见 docs/03-architecture.md 与 docs/08-operations.md：
// CLI / TUI / Web 三个壳都**不允许**自己实现业务逻辑，只能调用下层。
// 一旦允许某个壳「就这一个功能自己写一下」，三端行为漂移就开始了，
// 而且不可逆——用户会发现「Web 上能删的东西 CLI 删不掉」，然后不再
// 信任任何一端。
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/go/lib/buildinfo"
	"github.com/rzbdz/newgate/go/lib/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/roleprov"
)

// usageText 组装 `newgate --help`。
//
// **界面自己不认识任何一条命令、任何一个模块**：所有命令行都由拥有那项能力的
// 模块经 HelpLine 声明，这里只做组装与排版。上一版这里写死了一张 cmd(...) 清单，
// 结果是命令搬回模块之后界面还在硬编码它们——"搬了"等于白搬，而且每加一个模块
// 都得回来改界面（那正是本次重构要拆掉的东西）。
//
// 位置由命令自己声明的 Rank 决定；节的先后由槽位规则决定（见 extension.Section
// 与 PlanSections），节内再按 Rank、Usage 排。同 Rank 时靠 Usage 兜底，保证每次
// 跑出来顺序一致。
func usageText(service *service) string {
	var b strings.Builder
	b.WriteString(style.Bold("newgate") + " — AI CLI 的语义模型层代理\n")

	// 左列按显示宽度补齐（CJK 双宽），右列一律暗色——扫读时先看命令名，
	// 需要时再看说明。
	cmd := func(left, right string) {
		const commandWidth = 38
		prefix := "  " + style.Cyan(style.Pad(left, commandWidth))
		if right == "" {
			b.WriteString(prefix + "\n")
			return
		}
		lines := style.Wrap(style.Dim(right), style.MaxColumns-2-commandWidth)
		for i, line := range lines {
			if i == 0 {
				b.WriteString(prefix + line + "\n")
			} else {
				b.WriteString(strings.Repeat(" ", 2+commandWidth) + line + "\n")
			}
		}
	}

	// 收集：谁注入的命令，就由谁声明它在 help 里长什么样、放哪个**槽位**。
	//
	// 界面在这里**不认识任何一条命令、任何一个模块**：它只认识一组通用槽位键 +
	// 一条「自定义节先到先得、抢不到进 others」的规则（extension.PlanSections）。
	// 装一个新模块从不要求改这里一行。
	type entry struct {
		declared Section
		line     HelpLine
	}
	var entries []entry
	if service != nil {
		for _, c := range service.commands.All() {
			doc, ok := c.(Documented)
			if !ok {
				continue // 可选接口：没声明就不占一行，但仍然能用
			}
			line := doc.Help()
			if line.Usage == "" {
				continue
			}
			entries = append(entries, entry{line.Section, line})
		}
	}
	// 规划按**声明顺序**做（= 命令注册顺序），因为自定义节的名额是先到先得的。
	declared := make([]Section, 0, len(entries))
	for _, e := range entries {
		declared = append(declared, e.declared)
	}
	plan := PlanSections(declared)

	// 同一个显示节里的行按 Rank 再按 Usage 排；节的先后由 plan 决定，不参与
	// Rank 竞争——否则掰小自己的 Rank 就能把整节搬到最前面，槽位表就白设了。
	grouped := map[Section][]HelpLine{}
	for _, e := range entries {
		slot := plan.Slot(e.declared)
		grouped[slot] = append(grouped[slot], e.line)
	}
	for _, slot := range plan.Order() {
		lines := grouped[slot]
		if len(lines) == 0 {
			continue
		}
		sort.Slice(lines, func(i, j int) bool {
			if lines[i].Rank != lines[j].Rank {
				return lines[i].Rank < lines[j].Rank
			}
			return lines[i].Usage < lines[j].Usage
		})
		b.WriteString("\n" + style.Bold(slot) + "\n")
		for _, line := range lines {
			cmd(line.Usage, line.Summary)
		}
	}

	// 术语表是**界面自己的**东西：用户不知道某个词是什么意思时看的字典。它不是
	// 命令行清单（那是模块的），所以留在这里；能推导的一律现取（agent 名、槽位键
	// 都来自注册表），不写死。
	b.WriteString("\n" + style.Bold("术语") + "\n")
	term := func(left, right string) {
		b.WriteString("  " + style.Pad(style.Cyan(left), 12) + style.Dim(right) + "\n")
	}
	term("agent", "被接管的 CLI："+agentNames(service))
	term("profile", "一套「档位 → provider/模型」绑定")
	term("槽位键", slotTerm())
	b.WriteString("\n" + style.Bold("配置") + "\n")
	b.WriteString("  " + style.Dim("~/.config/newgate/ · providers.json · mappings/*.kv · state.json") + "\n")
	return b.String()
}

// agentNames 已知的 agent 名，**从注册表读**。
//
// 术语表原来写死「claude / opencode」——客户端 id 是各客户端模块的键，CLI 不该
// 知道任何一个（2026-09-18）。装一个新客户端，帮助文本不该需要跟着改。
func agentNames(service *service) string {
	if service == nil || service.agents == nil {
		return "（未装配）"
	}
	names := service.agents.Names()
	if len(names) == 0 {
		return "（无）"
	}
	return strings.Join(names, " / ")
}

// slotTerm 术语表里「槽位键」那一行的例子。键名由模块贡献（见
// domain.ExtraRole），所以例子也现取——原来写死的 omo-sisyphus / cat-deep
// 是 opencode-omo 的键，删掉那个模块之后帮助里就会留一个不存在的键。
func slotTerm() string {
	// 现刷一次动态角色表。它平时只在 store.Load（装配置快照）时刷新，而
	// `--help` 不装快照——不刷新的话这里读到的永远是空的，帮助里那行例子
	// 就会静默消失（2026-09-18 实测：改成现取之后例子没了）。读失败不挡
	// 帮助（失败开放），最差是那行退化成不带例子的说法。
	_ = roleprov.Refresh()

	var keys []string
	for _, r := range domain.ExtraRoles() {
		if r.Source == "builtin" {
			continue // 内置别名（normal→mid）是向下兼容，不是「槽位键」的例子
		}
		keys = append(keys, r.Key)
	}
	if len(keys) == 0 {
		return "模块贡献的动态角色，写法同档位"
	}
	if len(keys) > 2 {
		keys = keys[:2]
	}
	return "模块贡献的动态角色（" + strings.Join(keys, " / ") + "），写法同档位"
}

func runCLI(service *service, args []string) int {
	if shouldAuditLayout(service.agents, args) {
		return auditLayout(args, func() int { return run(service, args) })
	}
	return run(service, args)
}

func run(service *service, args []string) int {
	if len(args) == 0 {
		fmt.Print(usageText(service))
		return 0
	}

	// 动作型选项，可出现在任意位置
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--set-profile" || strings.HasPrefix(args[i], "--set-profile="):
			name := optValue(args, i, "--set-profile")
			if name == "" {
				return die(64, "--set-profile 需要一个 profile 名")
			}
			// 认得出这个选项的形状（argv 解析是界面的活），但**语义归 config**：
			// 归一成 config 那条命令认识的形状再交给它。
			command, ok := service.moduleCommand("--set-profile")
			if !ok {
				return die(64, "--set-profile 没有实现（装配缺了 config 模块）")
			}
			rest := []string{name}
			if agent := findFlag(args, "--agent", "--tool", "--target"); agent != "" {
				rest = append(rest, "--agent", agent)
			}
			return command.Run(moduleCLIHost{}, rest)
		}
	}

	// 包装启动：newgate claude --profile=ds / newgate --profile ds claude /
	// newgate run claude（docs/08-operations.md）。在账本查表之前判断——agent 名
	// 与命令名是互斥的封闭集合，不会撞车。
	if _, ok := detectLaunch(service.agents, args); ok {
		return cmdLaunch(service.runtime, service.agents, args)
	}

	// 账本查表：模块注入的命令与界面自己的命令走同一条路。
	//
	// **界面在这里不认识任何一条命令**——它只知道"有人往这个账本里挂过东西"。
	// 上一版这里是一个几百行的 switch，每一条都是一次「界面认识某个模块」，
	// 那批命令因此永远搬不回自己的模块（见 modules/cli/commands.go 的说明）。
	if command, ok := service.moduleCommand(args[0]); ok {
		return command.Run(moduleCLIHost{}, args[1:])
	}
	return die(64, fmt.Sprintf("未知命令 %q（newgate --help）", args[0]))
}

// VersionLine 是 `newgate version` 的输出（版式与取值都在 lib/buildinfo）。
func VersionLine() string { return buildinfo.VersionLine() }

// ---------- 小工具 ----------

// die 是界面自己的报错出口（版式在 lib/style，各模块共用同一份）。
func die(code int, msg string) int { return style.Die(code, msg) }

func has(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func arg(ss []string, i int) string {
	if i < len(ss) && !strings.HasPrefix(ss[i], "-") {
		return ss[i]
	}
	return ""
}

func intArg(ss []string, i, def int) int {
	if v := arg(ss, i); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func optValue(args []string, i int, name string) string {
	if strings.Contains(args[i], "=") {
		return strings.SplitN(args[i], "=", 2)[1]
	}
	if i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func findFlag(args []string, names ...string) string {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, n+"=") {
				return strings.SplitN(a, "=", 2)[1]
			}
		}
	}
	return ""
}

func intFlag(args []string, name string, def int) int {
	if v := findFlag(args, name); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func truthy(s string) bool { return s == "on" || s == "1" || s == "true" || s == "yes" }
