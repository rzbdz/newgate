package cli

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/runtime/launch"
	"github.com/rzbdz/newgate/go/internal/store"
)

// detectLaunch 判断这串 argv 是否像「包装启动」，而不是控制面子命令。
//
// 命中条件（docs/03 §1）：
//   - 第一个 token 是已知 agent（`newgate claude`）；
//   - 或 `run`（显式包装路径，`newgate run claude`）；
//   - 或以 `--profile` / `--preset` 开头（`newgate --profile ds claude`）。
//
// 返回的 string 是检测到的 agent 名（可能为空，交给 cmdLaunch 再解析）。
func detectLaunch(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	a0 := args[0]
	if a0 == "run" {
		return "", true
	}
	if _, ok := agents.Get(a0); ok {
		return a0, true
	}
	switch {
	case a0 == "--profile" || strings.HasPrefix(a0, "--profile="),
		a0 == "--preset" || strings.HasPrefix(a0, "--preset="):
		return "", true
	}
	return "", false
}

// cmdLaunch 包装启动一个 agent：解析 newgate 自己的选项，透传其余参数。
//
// 支持（docs/03 §2）：
//
//	newgate claude
//	newgate claude --profile=ds
//	newgate --profile ds claude
//	newgate run claude --profile=ds
//	newgate claude --resume abc          # --resume abc 透传给 claude
func cmdLaunch(args []string) int {
	agentName, profile, passthrough, err := splitLaunch(args)
	if err != nil {
		return die(64, err.Error())
	}
	a, _ := agents.Get(agentName)

	// profile 不存在立刻报错，绝不静默回落到默认——那正是「切了没生效」的来源。
	if profile != "" {
		if _, err := store.LoadProfile(profile); err != nil {
			avail, _ := store.ListProfiles()
			return die(65, fmt.Sprintf("profile %q 不存在（可用：%s）",
				profile, strings.Join(avail, ", ")))
		}
	}

	return launch.Launch(a, passthrough, launch.Options{Profile: profile})
}

// splitLaunch 把启动 argv 切成 (agent, profile, 透传参数)。
//
// 规则（docs/03 §2 的落地，唯一放宽：`--profile`/`--preset` 是 newgate 自己的
// 选项，出现在 agent 之后也照样被我们消费——否则 `newgate claude --profile=ds`
// 里的 profile 会被透传给 claude）：
//
//  1. 从左往右扫；
//  2. `--profile`/`--preset` 按 arity 消费值（`--profile ds` 或 `--profile=ds`）；
//  3. 第一个既不是 newgate 选项、也不是其值的 token 是 agent；
//  4. agent 之后、且不是 newgate 选项的 token 一律原样透传；
//  5. agent 确定之前的未知 `-` 选项按拼错处理（报错）。
func splitLaunch(args []string) (agent, profile string, passthrough []string, err error) {
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
					"未知选项 %q（tool 之前出现的未知选项按拼错处理，docs/03 §2 规则5）", a)
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
