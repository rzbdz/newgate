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
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/lib/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/roleprov"
)

// Version 由 main 注入。
var (
	Version    = "dev"
	BuildTime  = "unknown"
	CommitTime = "unknown"
)

// coreSection 这几节是 CLI 自己的动词（接管、跑一次、路由与配置、探测与观测、
// 维护）；模块声明的 Section 若命中它们就并进去，否则新开一节。
func coreSection(name string) bool {
	switch name {
	case "接管", "跑一次（不改全局状态）", "路由与配置", "探测与观测", "维护", "术语", "配置":
		return true
	}
	return false
}

// usageText 帮助。
//
// 左列固定宽度、右列是**一句话结论**，细节再往里塞就会变成没人读的墙。
// 分组按用户此刻想干什么排（接管 / 跑一次 / 路由 / 观测 / 维护），不按
// 代码里的文件排——用户不知道也不关心命令实现在哪个文件。
// usageText 组装 `newgate --help`。
//
// **界面自己不认识任何一条命令、任何一节**：所有命令行都由拥有那项能力的模块
// 经 HelpLine 声明，这里只做组装与排版。上一版这里写死了一张 cmd(...) 清单，
// 结果是命令搬回模块之后界面还在硬编码它们——"搬了"等于白搬，而且每加一个模块
// 都得回来改界面（那正是本次重构要拆掉的东西）。
//
// 位置由命令自己声明的 Rank 决定：节的顺序取该节最小的 Rank，节内再按 Rank、
// Usage 排。同 Rank 时靠 Usage 兜底，保证每次跑出来顺序一致。
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

	// 收集：谁注入的命令，就由谁声明它在 help 里长什么样、放哪个位置。
	sections := map[string][]HelpLine{}
	var order []string
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
			if _, seen := sections[line.Section]; !seen {
				order = append(order, line.Section)
			}
			sections[line.Section] = append(sections[line.Section], line)
		}
	}
	// 节的顺序 = 该节最小的 Rank。空 Section（不分组）排最后：它没有位置主张。
	rankOf := func(name string) int {
		best := -1
		for _, line := range sections[name] {
			if best < 0 || line.Rank < best {
				best = line.Rank
			}
		}
		return best
	}
	sort.Slice(order, func(i, j int) bool {
		ri, rj := rankOf(order[i]), rankOf(order[j])
		if ri != rj {
			return ri < rj
		}
		return order[i] < order[j]
	})

	for _, name := range order {
		lines := sections[name]
		sort.Slice(lines, func(i, j int) bool {
			if lines[i].Rank != lines[j].Rank {
				return lines[i].Rank < lines[j].Rank
			}
			return lines[i].Usage < lines[j].Usage
		})
		if name != "" {
			b.WriteString("\n" + style.Bold(name) + "\n")
		} else {
			b.WriteString("\n")
		}
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
			return cmdSetProfile(findFlag(args, "--agent", "--tool", "--target"), name)
		}
	}

	// 包装启动：newgate claude --profile=ds / newgate --profile ds claude /
	// newgate run claude（docs/08-operations.md）。在子命令 switch 之前判断——agent 名
	// 与子命令名是互斥的封闭集合，不会撞车。
	if _, ok := detectLaunch(service.agents, args); ok {
		return cmdLaunch(service.runtime, service.agents, args)
	}
	if command, ok := service.moduleCommand(args[0]); ok {
		return command.Run(moduleCLIHost{}, args[1:])
	}

	switch args[0] {
	// start / stop 带 agent 名就是单独接管/释放那一个，不带就是全面接管/停止。
	// 用户不需要知道背后是 PATH shim 还是改配置文件——那是我们的实现细节。
	case "start":
		if a := arg(args, 1); a != "" {
			return cmdTakeover(service.agents, a)
		}
		return cmdStart(service.agents, has(args, "--force"))
	case "stop":
		if a := arg(args, 1); a != "" {
			return cmdRelease(a)
		}
		return cmdStop()
	case "on", "takeover", "take":
		return cmdTakeover(service.agents, arg(args, 1))
	case "off", "release", "free":
		if a := arg(args, 1); a != "" {
			return cmdRelease(a)
		}
		return cmdStop()
	case "restart":
		return cmdRestart(service.agents, has(args, "--force"))
	case "status":
		return cmdStatus(service.agents, service)
	case "reload":
		return cmdReload()
	case "profiles", "ls":
		return cmdProfiles()
	case "profile":
		if len(args) > 1 && args[1] == "kv" {
			return cmdProfileKV(args[2:])
		}
		return die(64, "用法：newgate profile kv <名> [--write]")
	case "tier", "tiers", "role", "roles":
		return cmdTier(args[1:])
	case "probe":
		return cmdProbe(arg(args, 1), has(args, "--json"))
	case "breaker", "breakers":
		return cmdBreaker()
	case "metrics", "stat":
		return cmdMetrics()
	case "shim":
		// 不给默认 agent：客户端 id 是**各客户端模块自己的键**，cli 不该知道
		// 任何一个（2026-09-18 之前这里写死 "claude"）。逃生口本来也该显式。
		return cmdShim(service.agents, arg(args, 1), arg(args, 2))
	case "agents":
		return cmdAgents(service.agents)
	case "init":
		return cmdInit(has(args, "--force"))
	case "doctor":
		return cmdDoctor(service)
	case "logs", "log":
		return cmdLogs(logCount(args), has(args, "-f") || has(args, "--follow"))
	case "alllogs", "all-logs":
		return cmdAllLogs(service.agents, service)
	case "tui", "menuconfig":
		return cmdTUI()
	case "version", "--version", "-v":
		fmt.Println(VersionLine())
		return 0
	case "help", "--help", "-h":
		fmt.Print(usageText(service))
		return 0
	case "__serve":
		return Serve(service, intFlag(args, "--port", 0))
	default:
		return die(64, fmt.Sprintf("未知命令 %q（newgate --help）", args[0]))
	}
}

func VersionLine() string {
	return fmt.Sprintf("newgate %s\n  构建于 %s\n  提交于 %s",
		Version, buildTimeDisplay(), stampDisplay(CommitTime))
}

// pretty ldflags 传不了空格，用下划线占位，展示时换回。
func pretty(s string) string { return strings.ReplaceAll(s, "_", " ") }

// buildTimeDisplay 构建时间的展示串。
func buildTimeDisplay() string { return stampDisplay(BuildTime) }

// stampDisplay 把 ldflags 里的时间戳渲染成**一个**本地时间。
//
// 2026-09-18 之前这里印两个时间（UTC 原文 + 换算出本地），因为 Makefile 用
// `date -u` 打 UTC，而 commitTime 走 git 的本地时间——同一行两个时区，看的人
// 得自己换算。现在 Makefile 统一打本地时区（`%z` 带偏移），这里也就只印一个。
// 解析不了（dev 构建没注入，值是 "unknown"）就原样回，不硬凑。
func stampDisplay(s string) string {
	if t, err := time.Parse("2006-01-02_15:04:05Z0700", s); err == nil {
		return t.Local().Format("2006-01-02 15:04:05 MST")
	}
	return pretty(s)
}

// ---------- 小工具 ----------

func die(code int, msg string) int {
	fmt.Fprintln(os.Stderr, style.WrapLine("newgate: "+msg, "  "))
	return code
}

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
