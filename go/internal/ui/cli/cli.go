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
)

// Version 由 main 注入。
var (
	Version    = "dev"
	BuildTime  = "unknown"
	CommitTime = "unknown"
)

const usage = `newgate — AI agent 配置的语义命名层

用法:
  newgate start [--force]        启动代理 + 接管已配置的 agent
  newgate stop                   停代理 + 还原配置（逃生舱）
  newgate restart
  newgate status                 当前状态：每个 agent 用哪个 profile
  newgate reload                 立刻重读配置（平时会自动热更新，无需此命令）

  newgate --set-profile <名> [--agent <agent>]
                                 切某个 agent 的 profile；省略 --agent 设全局默认
  newgate profiles               列出所有 profile
  newgate tier <档位>            看这个档位的 fallback 链：每个候选为什么选中/跳过
  newgate probe [profile]        给每个候选打一次真实请求，出健康报告

  newgate shim install|off|on|uninstall|status [agent]
                                 PATH shim：装上之后直接敲 claude 就走 newgate
  newgate agents                 列出已知 agent 及其槽位

  newgate init [--force]         铺开默认配置
  newgate doctor                 体检
  newgate logs [N] [-f]          代理日志
  newgate alllogs                完整诊断包
  newgate debug on|off [分钟]    全量请求日志（默认 30 分钟后自动关）
  newgate schema-repair on|off
  newgate tui                    menuconfig 风格界面
  newgate version

术语:
  agent        被接管的 CLI —— claude / opencode
  intra-agent  agent 内部的子 agent —— 西西弗斯 / oracle
  tier         能力档 —— heavy / mid / light / vision
  model        真实模型

配置: ~/.config/newgate/{providers.json, mappings/*.json, state.json}
`

// Run 是 CLI 的唯一入口。
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
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

	switch args[0] {
	case "start":
		return cmdStart(has(args, "--force"))
	case "stop":
		return cmdStop()
	case "restart":
		cmdStop()
		return cmdStart(has(args, "--force"))
	case "status":
		return cmdStatus()
	case "reload":
		return cmdReload()
	case "profiles", "ls":
		return cmdProfiles()
	case "tier", "tiers", "role", "roles":
		return cmdTier(arg(args, 1))
	case "probe":
		return cmdProbe(arg(args, 1), has(args, "--json"))
	case "shim":
		return cmdShim(arg(args, 1), argOr(args, 2, "claude"))
	case "agents":
		return cmdAgents()
	case "init":
		return cmdInit(has(args, "--force"))
	case "doctor":
		return cmdDoctor()
	case "logs", "log":
		return cmdLogs(intArg(args, 1, 40), has(args, "-f") || has(args, "--follow"))
	case "alllogs", "all-logs":
		return cmdAllLogs()
	case "debug":
		return cmdDebug(args)
	case "schema-repair", "schema_repair":
		return cmdSchemaRepair(len(args) > 1 && truthy(args[1]))
	case "tui", "menuconfig":
		return cmdTUI()
	case "version", "--version", "-v":
		fmt.Println(VersionLine())
		return 0
	case "help", "--help", "-h":
		fmt.Print(usage)
		return 0
	case "__serve":
		return Serve(intFlag(args, "--port", 0))
	default:
		return die(64, fmt.Sprintf("未知命令 %q（newgate --help）", args[0]))
	}
}

func VersionLine() string {
	return fmt.Sprintf("newgate %s\n  构建于 %s\n  提交于 %s",
		Version, pretty(BuildTime), pretty(CommitTime))
}

// pretty ldflags 传不了空格，用下划线占位，展示时换回。
func pretty(s string) string { return strings.ReplaceAll(s, "_", " ") }

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
