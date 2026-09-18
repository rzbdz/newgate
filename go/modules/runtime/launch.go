package runtime

// 本文件是**包装启动**：`newgate claude …` / `newgate run claude …` /
// `newgate --profile ds claude`。2026-09-18 从 modules/cli/launch.go 整体搬来。
//
// 为什么搬：「把某个客户端拉起来，并把它需要的 env/argv 组装好」就是本模块的
// 定义（见 package runtime 的说明）。原处的注释写着「runtime 只负责找到被 shim
// 遮住的真实二进制、构造最小环境覆盖并启动它」——而这段代码正是那条规则的调用
// 方，它留在界面里只因为界面是当初敲命令的地方。
//
// 界面因此不再需要知道「有哪些 agent」才能启动一个：客户端 id 是本模块与
// config-hook 的词汇。它把 `newgate claude …` 这一行交给账本里名字叫 claude 的
// 命令——那条命令就是下面的 launchCommand，名字由它自己声明（各 agent 一个）。

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/store"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
)

// launchCommand 包装启动一个 agent。
//
// **每个已知 agent 一个实例**，各自带着自己的 id；外加一个不带 id 的实例负责显式
// 的 `run <agent>`。这是账本第一次被用来声明「一个模块认识的若干个动词」而不是一
// 个固定动词——分派器不需要为此改一行，它本来就只是查表。
//
// 为什么要按 agent 拆实例（2026-09-18 实测踩到）：分派器交给命令的 args **不含
// 动词本身**（契约如此）。`newgate claude --profile=ds` 分派到 `claude` 这条命令时
// 收到的是 `["--profile=ds"]`——agent 名在动词位上被剥掉了。命令自己知道自己是
// 谁，把 id 补回去即可；一个实例包办所有 id 的话，那一发就会报「要启动哪个 agent」。
type launchCommand struct {
	rt     Runtime
	agents confighookapi.AgentCatalog
	// agentID 非空 = 这个实例只服务这一个 agent（`newgate claude …`）；
	// 为空 = 显式路径 `newgate run <agent> …`，agent 名在参数里。
	agentID string
}

var (
	_ cliapi.Command    = (*launchCommand)(nil)
	_ cliapi.Documented = (*launchCommand)(nil)
	_ cliapi.Handoff    = (*launchCommand)(nil)
)

// Names 每个已知 agent 一个名字，外加 `run`（`newgate run claude`）。
//
// 现取而不是缓存：Attach 时目录已经装好了（config-hook 排在前面），而客户端
// 模块可能在 Attach 之后才注册——现取才不会漏（分派器是每次命令现查账本的）。
func (c launchCommand) Names() []string {
	if c.agentID != "" {
		return []string{c.agentID}
	}
	return []string{"run"}
}

// Help 只在显式路径 `run` 那一行出现。
//
// **故意不逐个 agent 列一行**：agent 名是各客户端模块的键，help 里列出它们等于
// 界面又认识了一遍客户端。用户敲 `newgate claude` 从来不是从 help 里学来的。
//
// 实现方式是按 agentID 返回空 Usage（usageText 约定「没有 Usage 就不占一行」）：
// 每个 agent 一个实例是**分派键**，不是 help 条目。2026-09-18 之前这里无差别
// 返回同一行，而 launchCommands 给每个 agent 都注册了一个实例——于是
// `newgate --help` 里 `run <agent> [args…]` 原样重复了 N 遍（实测 2 个 agent
// 时 3 行）。注释说着「只出现一次」，代码没有做到，这正是这个仓库最怕的那种
// 不一致：读注释的人不会去数。
func (c launchCommand) Help() cliapi.HelpLine {
	if c.agentID != "" {
		return cliapi.HelpLine{} // 空 Usage = 不占行（见 cli.usageText）
	}
	return cliapi.HelpLine{Section: cliapi.SectionRunOnce, Rank: 20,
		Usage: "run <agent> [args…]", Summary: "用某个 profile 跑一次"}
}

func (c launchCommand) Run(_ cliapi.Host, args []string) int {
	// 动词位被分派器剥掉了：名字就是 agent 的那种调用要把 id 补回参数首位，
	// 让 splitLaunch 走同一条路径（它也负责校验 id 是不是真的存在）。
	if c.agentID != "" {
		args = append([]string{c.agentID}, args...)
	}
	return runLaunch(c.rt, c.agents, args)
}

// launchCommands 每个已知 agent 一条，外加显式的 run。
func launchCommands(rt Runtime, agents confighookapi.AgentCatalog) []cliapi.Command {
	out := []cliapi.Command{launchCommand{rt: rt, agents: agents}}
	for _, id := range agents.Names() {
		out = append(out, launchCommand{rt: rt, agents: agents, agentID: id})
	}
	return out
}

// HandsOff：这条命令接着会把控制权交给被启动的客户端，newgate 的版式审计不该
// 去看那个进程的输出（见 cliapi.Handoff）。
func (launchCommand) HandsOff() {}

// cmdLaunch 包装启动一个 agent：解析 newgate 自己的选项，透传其余参数。
//
// 支持（docs/08-operations.md）：
//
//	newgate claude
//	newgate claude --profile=ds
//	newgate --profile ds claude
//	newgate run claude --profile=ds
//	newgate claude --resume abc          # --resume abc 透传给 claude
func runLaunch(rt Runtime, agents confighookapi.AgentCatalog, args []string) int {
	agentName, profile, passthrough, err := splitLaunch(agents, args)
	if err != nil {
		return style.Die(64, err.Error())
	}
	a, _ := agents.Get(agentName)

	// profile 不存在立刻报错，绝不静默回落到默认——那正是「切了没生效」的来源。
	if profile != "" {
		if _, err := store.LoadProfile(profile); err != nil {
			avail, _ := store.ListProfiles()
			return style.Die(65, fmt.Sprintf("profile %q 不存在（可用：%s）",
				profile, strings.Join(avail, ", ")))
		}
	}

	return rt.Launch(a, passthrough, profile)
}

// splitLaunch 把启动 argv 切成 (agent, profile, 透传参数)。
//
// 规则（docs/08-operations.md 的落地，唯一放宽：`--profile`/`--preset` 是 newgate 自己的
// 选项，出现在 agent 之后也照样被我们消费——否则 `newgate claude --profile=ds`
// 里的 profile 会被透传给 claude）：
//
//  1. 从左往右扫；
//  2. `--profile`/`--preset` 按 arity 消费值（`--profile ds` 或 `--profile=ds`）；
//  3. 第一个既不是 newgate 选项、也不是其值的 token 是 agent；
//  4. agent 之后、且不是 newgate 选项的 token 一律原样透传；
//  5. agent 确定之前的未知 `-` 选项按拼错处理（报错）。
func splitLaunch(agents confighookapi.AgentCatalog, args []string) (agent, profile string, passthrough []string, err error) {
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}

	var rest []string
	agentKnown := false

	for i := 0; i < len(args); i++ {
		a := args[i]

		// newgate 自己的 profile 选项（`--preset` 是历史别名，同一件事）。
		if a == "--profile" || a == "--preset" || strings.HasPrefix(a, "--profile=") || strings.HasPrefix(a, "--preset=") {
			v := ""
			if j := strings.Index(a, "="); j >= 0 {
				v = a[j+1:]
			} else if i+1 < len(args) {
				v = args[i+1]
				i++
			}
			if v == "" {
				return "", "", nil, fmt.Errorf("%s 需要一个 profile 名", a)
			}
			if profile != "" && profile != v {
				return "", "", nil, fmt.Errorf("同时给了 profile %q 和 %q，不一致", profile, v)
			}
			profile = v
			continue
		}

		if strings.HasPrefix(a, "-") {
			if !agentKnown {
				return "", "", nil, fmt.Errorf(
					"未知选项 %q（tool 之前出现的未知选项按拼错处理，docs/08-operations.md 规则5）", a)
			}
			rest = append(rest, a)
			continue
		}

		if !agentKnown {
			agent = a
			agentKnown = true
		} else {
			rest = append(rest, a)
		}
	}

	if agent == "" {
		return "", "", nil, fmt.Errorf("要启动哪个 agent？例：newgate claude 或 newgate run claude")
	}
	if _, ok := agents.Get(agent); !ok {
		return "", "", nil, fmt.Errorf("不认识的 agent %q（已知：%s）",
			agent, strings.Join(agents.Names(), ", "))
	}
	return agent, profile, rest, nil
}
