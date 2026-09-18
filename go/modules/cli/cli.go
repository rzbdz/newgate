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
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/lib/style"
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
func usageText(service *service) string {
	var b strings.Builder
	b.WriteString(style.Bold("newgate") + " — AI CLI 的语义模型层代理\n")

	sec := func(t string) { b.WriteString("\n" + style.Bold(t) + "\n") }
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
	raw := func(line string) { b.WriteString("  " + line + "\n") }

	// 模块贡献的 help 行：按它们**自己声明的** Section 归位。
	//
	// 这一步是「命令搬回各模块」的另一半。命令搬走了而 help 不搬，等于 cli 还
	// 认识那个模块——`Documented` 端口存在就是为了这个，不消费它等于没搬。
	//
	// 认不出的 Section 会新开一节，排在已知几节之后。**不报错**：节名是呈现
	// 概念，一个模块想给自己新开一节是正当的，为它把 --help 打崩才荒唐。
	contrib := map[string][]HelpLine{}
	var extra []string
	if service != nil {
		for _, c := range service.commands.All() {
			doc, ok := c.(Documented)
			if !ok {
				continue
			}
			line := doc.Help()
			if line.Usage == "" {
				continue
			}
			if _, known := contrib[line.Section]; !known && !coreSection(line.Section) {
				extra = append(extra, line.Section)
			}
			contrib[line.Section] = append(contrib[line.Section], line)
		}
	}
	emit := func(section string) {
		for _, line := range contrib[section] {
			cmd(line.Usage, line.Summary)
		}
	}

	sec("接管")
	cmd("start", "起代理 + 接管所有 agent")
	cmd("stop", "停代理 + 所有 agent 恢复直连")
	cmd("on <agent>", "只接管一个")
	cmd("off <agent>", "只放开一个，以后 start 也不再管它")
	cmd("restart", "重启代理，接管现场原样保留")
	cmd("status", "谁在走 newgate、用哪个 profile")
	cmd("reload", "立刻重读配置（平时 1 秒内自动热更新）")
	emit("接管")

	sec("跑一次（不改全局状态）")
	cmd("<agent> [--profile <名>] [args…]", "用某个 profile 跑一次")
	cmd("run <agent> [args…]", "同上，显式写法")
	cmd("newgate-<名> <agent> …", "argv0 分发，等价 --profile <名>")

	sec("路由与配置")
	cmd("tier [档位]", "fallback 链：走谁、跳过了什么")
	cmd("profiles", "所有 profile（优先级 / 标志 / 覆盖）")
	cmd("--set-profile <名> [--agent <agent>]", "切 profile；省略 --agent 设全局默认")
	cmd("profile kv <名> [--write]", "profile 转 KV 文本")
	cmd("agents", "已知 agent 及其模型槽位")
	emit("路由与配置")

	sec("探测与观测")
	cmd("probe [profile]", "给候选打真实请求，出健康报告")
	cmd("breaker", "哪些 binding 被摘牌了、为什么、多久了")
	cmd("metrics", "代理计数器：拦截 / 超时 / 转移 / 改道")
	cmd("doctor", "体检")
	cmd("logs [N] [-f]", "代理日志：终端上分页，-f 持续跟随")
	cmd("alllogs", "完整诊断包")
	cmd("debug on|off [分钟]", "全量请求日志（默认 30 分钟自动关）")
	emit("探测与观测")

	sec("维护")
	cmd("init [--force]", "铺开默认配置")
	cmd("shim …", "底层逃生口，平时用 on/off 就够了")
	cmd("tui", "menuconfig 风格界面")
	cmd("version", "")
	emit("维护")

	// 模块自开的节。
	for _, name := range extra {
		sec(name)
		emit(name)
	}

	sec("术语")
	term := func(left, right string) {
		b.WriteString("  " + style.Pad(style.Cyan(left), 10) + style.Dim(right) + "\n")
	}
	term("agent", "被接管的 CLI：claude / opencode")
	term("tier", "能力档 heavy > normal（主力）> mid > light，另加正交的 vision")
	term("profile", "一套「档位 → provider/模型」绑定")
	term("槽位键", "模块贡献的动态角色（omo-sisyphus / cat-deep），写法同档位")
	term("st", "special_treatment：只对某家上游生效的请求补丁")
	term("plugin", "模块分类（infra/gateway/client/model/…）与它的运行期开关点")

	sec("配置")
	raw(style.Dim("~/.config/newgate/ · providers.json · mappings/*.kv · state.json"))
	return b.String()
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
	case "debug":
		return cmdDebug(args)
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
