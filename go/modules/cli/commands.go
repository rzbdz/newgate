package cli

import (
	"fmt"
)

// 本文件把界面**自己的**那批命令也做成 Command，注册进同一个账本。
//
// 为什么（2026-09-18）：这些命令还住在界面里（它们要读配置、接管状态、守护进程
// 生命周期，而那些依赖还没从界面搬走），但它们的**声明**没必要和界面绑在一起。
// 做成 Command 之后：
//
//   - `run()` 里那个巨大的 switch 消失了，只剩编排（argv0 分发、包装启动、账本
//     查表、未知命令兜底）；
//   - `--help` 自动补全——每个命令自己声明 HelpLine，不必再在 usageText 里维护
//     一张并列的清单（那两张表错开过一次：命令搬走了、help 还留着）；
//   - 每条命令都变成了一个**可以整体搬走**的单元：哪天它的依赖也离开了界面，
//     把这个类型连同它的 cmdXxx 一起挪到拥有它的模块即可。
//
// 留在这里的判断标准写在 CLAUDE.md §4：把它删掉，业务模块还成立吗？成立就不该
// 留在界面——反过来说，这些命令**此刻**留在界面是因为它们依赖的东西还在界面手上。

// 帮助行的位置。数字留出空档，方便插队；节内按 Rank 再按 Usage 排。
const (
	rankTakeover = 10 // 接管
	rankRunOnce  = 20 // 跑一次
	rankRouting  = 30 // 路由与配置
	rankObserve  = 40 // 探测与观测
	rankMaint    = 50 // 维护
	rankSystem   = 60 // 界面自身（help / version）
)

type statusCommand struct{ s *service }

func (statusCommand) Names() []string { return []string{"status"} }
func (statusCommand) Help() HelpLine {
	return HelpLine{Section: SectionTakeover, Rank: rankTakeover,
		Usage: "status", Summary: "谁在走 newgate、用哪个 profile"}
}
func (c statusCommand) Run(_ Host, _ []string) int { return cmdStatus(c.s) }

// runOnceCommand 是 `newgate run <agent>` 的显式写法。
//
// 它没有 HelpLine 之外的行为：argv0 分发与 `--profile` 都走 main 与 cmdLaunch
// （见 run() 的编排）。这里注册它只是为了让它出现在 help 里、并且不被当成未知命令。
type runOnceCommand struct{ s *service }

func (runOnceCommand) Names() []string { return []string{"run"} }
func (runOnceCommand) Help() HelpLine {
	return HelpLine{Section: SectionRunOnce, Rank: rankRunOnce,
		Usage: "run <agent> [args…]", Summary: "用某个 profile 跑一次"}
}
func (c runOnceCommand) Run(_ Host, args []string) int {
	return cmdLaunch(c.s.runtime, c.s.agents, append([]string{"run"}, args...))
}

// ---------- 探测与观测 ----------

type breakerCommand struct{}

func (breakerCommand) Names() []string { return []string{"breaker", "breakers"} }
func (breakerCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "breaker", Summary: "哪些 binding 被摘牌了、为什么、多久了"}
}
func (breakerCommand) Run(_ Host, _ []string) int { return cmdBreaker() }

type doctorCommand struct{ s *service }

func (doctorCommand) Names() []string { return []string{"doctor"} }
func (doctorCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "doctor", Summary: "体检"}
}
func (c doctorCommand) Run(_ Host, _ []string) int { return cmdDoctor(c.s) }

type logsCommand struct{}

func (logsCommand) Names() []string { return []string{"logs", "log"} }
func (logsCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "logs [N] [-f]", Summary: "代理日志：终端上分页，-f 持续跟随"}
}
func (logsCommand) Run(_ Host, args []string) int {
	return cmdLogs(logCount(args), has(args, "-f") || has(args, "--follow"))
}

type allLogsCommand struct{ s *service }

func (allLogsCommand) Names() []string { return []string{"alllogs", "all-logs"} }
func (allLogsCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "alllogs", Summary: "完整诊断包"}
}
func (c allLogsCommand) Run(_ Host, _ []string) int { return cmdAllLogs(c.s.agents, c.s) }

// ---------- 界面自身 ----------

type versionCommand struct{}

func (versionCommand) Names() []string {
	return []string{"version", "--version", "-v"}
}
func (versionCommand) Help() HelpLine {
	return HelpLine{Section: SectionUI, Rank: rankSystem,
		Usage: "version", Summary: ""}
}
func (versionCommand) Run(_ Host, _ []string) int {
	fmt.Println(VersionLine())
	return 0
}

type helpCommand struct{ s *service }

func (helpCommand) Names() []string { return []string{"help", "--help", "-h"} }
func (helpCommand) Help() HelpLine {
	return HelpLine{Section: SectionUI, Rank: rankSystem,
		Usage: "help", Summary: "这一屏"}
}
func (c helpCommand) Run(_ Host, _ []string) int {
	fmt.Print(usageText(c.s))
	return 0
}

// serveCommand 是守护进程本体：`newgate __serve`（由 daemon.Spawn 拉起）。
//
// **故意没有 HelpLine**：它是内部入口，用户不该在 help 里看到它，也不该手敲。
type serveCommand struct{ s *service }

func (serveCommand) Names() []string { return []string{"__serve"} }
func (c serveCommand) Run(_ Host, args []string) int {
	return Serve(c.s, intFlag(args, "--port", 0))
}

// ownCommands 是界面自己的命令。
//
// 注册进同一个账本（而不是留在 run() 的 switch 里）：查重、分派、help 组装走
// 同一条路，run() 于是只剩编排。哪天某条命令的依赖离开了界面，把它从这里删掉、
// 挪到拥有它的模块即可——见文件头的说明。
func ownCommands(s *service) []Command {
	return []Command{
		statusCommand{s}, runOnceCommand{s},
		breakerCommand{}, doctorCommand{s},
		logsCommand{}, allLogsCommand{s},
		versionCommand{}, helpCommand{s}, serveCommand{s},
	}
}

// registerOwnCommands 把界面自己的命令挂进账本。撞名即装配期错误——两个模块
// 抢同一个命令名必须是当场报错，而不是先到先得。
func (s *service) registerOwnCommands() error {
	for _, c := range ownCommands(s) {
		if _, err := s.RegisterCommand(c); err != nil {
			return err
		}
	}
	return nil
}
