// Package cli 是命令行前端。
//
// 分层规则（docs/02-architecture.md §2、docs/12-frontends.md §1）：
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

	"github.com/rzbdz/newgate/go/internal/ui/style"
)

// Version 由 main 注入。
var (
	Version    = "dev"
	BuildTime  = "unknown"
	CommitTime = "unknown"
)

// usageText 帮助。
//
// 左列固定宽度、右列是**一句话结论**，细节再往里塞就会变成没人读的墙。
// 分组按用户此刻想干什么排（接管 / 跑一次 / 路由 / 观测 / 维护），不按
// 代码里的文件排——用户不知道也不关心命令实现在哪个文件。
func usageText() string {
	var b strings.Builder
	b.WriteString(style.Bold("newgate") + " — AI CLI 的语义模型层代理\n")

	sec := func(t string) { b.WriteString("\n" + style.Bold(t) + "\n") }
	// 左列按显示宽度补齐（CJK 双宽），右列一律暗色——扫读时先看命令名，
	// 需要时再看说明。
	cmd := func(left, right string) {
		b.WriteString("  " + style.Cyan(style.Pad(left, 38)) + style.Dim(right) + "\n")
	}
	raw := func(line string) { b.WriteString("  " + line + "\n") }

	sec("接管")
	cmd("start", "起代理 + 接管所有 agent")
	cmd("stop", "停代理 + 所有 agent 恢复直连")
	cmd("on <agent>", "只接管一个")
	cmd("off <agent>", "只放开一个，以后 start 也不再管它")
	cmd("restart", "重启代理，接管现场原样保留")
	cmd("status", "谁在走 newgate、用哪个 profile")
	cmd("reload", "立刻重读配置（平时 1 秒内自动热更新）")

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
	cmd("omo", "omo 槽位：现状 / 建议 / 覆盖（不带参数看列表）")

	sec("探测与观测")
	cmd("probe [profile]", "给候选打真实请求，出健康报告")
	cmd("metrics", "代理计数器：拦截 / 超时 / 转移 / 改道")
	cmd("doctor", "体检")
	cmd("logs [N] [-f]", "代理日志：终端上分页，-f 持续跟随")
	cmd("alllogs", "完整诊断包")
	cmd("debug on|off [分钟]", "全量请求日志（默认 30 分钟自动关）")
	cmd("st [on|off] [插件]", "special_treatment 开关与说明")

	sec("维护")
	cmd("init [--force]", "铺开默认配置")
	cmd("schema-repair on|off", "")
	cmd("shim …", "底层逃生口，平时用 on/off 就够了")
	cmd("tui", "menuconfig 风格界面")
	cmd("version", "")

	sec("术语")
	term := func(left, right string) {
		b.WriteString("  " + style.Pad(style.Cyan(left), 10) + style.Dim(right) + "\n")
	}
	term("agent", "被接管的 CLI：claude / opencode")
	term("tier", "能力档 heavy > normal（主力）> mid > light，另加正交的 vision")
	term("profile", "一套「档位 → provider/模型」绑定")
	term("槽位键", "模块贡献的动态角色（omo-sisyphus / cat-deep），写法同档位")
	term("st", "special_treatment：只对某家上游生效的请求补丁")

	sec("配置")
	raw(style.Dim("~/.config/newgate/ · providers.json · mappings/*.kv · state.json"))
	return b.String()
}

// Run 是 CLI 的唯一入口。
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usageText())
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
	// newgate run claude（docs/03 §1）。在子命令 switch 之前判断——agent 名
	// 与子命令名是互斥的封闭集合，不会撞车。
	if _, ok := detectLaunch(args); ok {
		return cmdLaunch(args)
	}

	switch args[0] {
	// start / stop 带 agent 名就是单独接管/释放那一个，不带就是全面接管/停止。
	// 用户不需要知道背后是 PATH shim 还是改配置文件——那是我们的实现细节。
	case "start":
		if a := arg(args, 1); a != "" {
			return cmdTakeover(a)
		}
		return cmdStart(has(args, "--force"))
	case "stop":
		if a := arg(args, 1); a != "" {
			return cmdRelease(a)
		}
		return cmdStop()
	case "on", "takeover", "take":
		return cmdTakeover(arg(args, 1))
	case "off", "release", "free":
		if a := arg(args, 1); a != "" {
			return cmdRelease(a)
		}
		return cmdStop()
	case "restart":
		return cmdRestart(has(args, "--force"))
	case "status":
		return cmdStatus()
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
	case "omo", "slots":
		return cmdOmo(args[1:])
	case "probe":
		return cmdProbe(arg(args, 1), has(args, "--json"))
	case "metrics", "stat":
		return cmdMetrics()
	case "shim":
		return cmdShim(arg(args, 1), argOr(args, 2, "claude"))
	case "agents":
		return cmdAgents()
	case "init":
		return cmdInit(has(args, "--force"))
	case "doctor":
		return cmdDoctor()
	case "logs", "log":
		return cmdLogs(logCount(args), has(args, "-f") || has(args, "--follow"))
	case "alllogs", "all-logs":
		return cmdAllLogs()
	case "debug":
		return cmdDebug(args)
	case "schema-repair", "schema_repair":
		return cmdSchemaRepair(len(args) > 1 && truthy(args[1]))
	case "st", "special", "special-treatment", "special_treatment":
		return cmdSpecial(args)
	case "tui", "menuconfig":
		return cmdTUI()
	case "version", "--version", "-v":
		fmt.Println(VersionLine())
		return 0
	case "help", "--help", "-h":
		fmt.Print(usageText())
		return 0
	case "__serve":
		return Serve(intFlag(args, "--port", 0))
	default:
		return die(64, fmt.Sprintf("未知命令 %q（newgate --help）", args[0]))
	}
}

func VersionLine() string {
	return fmt.Sprintf("newgate %s\n  构建于 %s\n  提交于 %s",
		Version, buildTimeDisplay(), pretty(CommitTime))
}

// pretty ldflags 传不了空格，用下划线占位，展示时换回。
func pretty(s string) string { return strings.ReplaceAll(s, "_", " ") }

// buildTimeDisplay 构建时间的展示串：ldflags 里的 UTC 时间 + 换算好的本地时间。
//
// Makefile 用 `date -u` 打 UTC 时间戳（结尾带 Z），光看它容易算错本地几点；
// 这里顺手算一份本地时区并列出来，一眼对上「这二进制是我几点几分编的」。
// 解析失败（dev 构建没注入时间 = "unknown"）就只回原文，不硬凑。
func buildTimeDisplay() string {
	utc := pretty(BuildTime)
	if t, err := time.Parse("2006-01-02_15:04:05Z07:00", BuildTime); err == nil {
		return fmt.Sprintf("%s（本地 %s）", utc, t.Local().Format("2006-01-02 15:04:05 MST"))
	}
	return utc
}

// ---------- 小工具 ----------

func die(code int, msg string) int {
	fmt.Fprintf(os.Stderr, "newgate: %s\n", msg)
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

func argOr(ss []string, i int, def string) string {
	if v := arg(ss, i); v != "" {
		return v
	}
	return def
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
