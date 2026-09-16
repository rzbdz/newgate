package resolve

import "github.com/rzbdz/newgate/go/internal/core/domain"

// PrimaryBinding 返回某个档位在当前链头下**实际会用到的第一个候选**。
//
// 用途：wrapper 启动 agent 前，用它把每个槽位 env 填成真实模型名（而不是
// 档位名），让 Claude Code 这类工具在界面里显示真实模型。因此这里**忽略**
// 运行时可用性（熔断器、禁用）——显示的是「配置里排在第一个的」，实际转发
// 时的 fallback 由代理在请求时用 BuildChain + 熔断器重新决定，两者不一致时
// 以 X-Newgate-Route 响应头为准。
func PrimaryBinding(tier string, profiles []*domain.Profile, provs *domain.Providers, active string) (domain.Binding, bool) {
	steps, _ := BuildChain(tier, profiles, provs, Opts{Active: active})
	if len(steps) == 0 {
		return domain.Binding{}, false
	}
	return steps[0].Binding, true
}

// realNameRoleOrder 真实模型名反查档位时的优先序。
//
// 为什么不按 Roles 原序（heavy 在前）：档位名请求（model:"heavy"）的 tier
// 是客户端明说的，而真实名请求的 tier 只能反查——名字在线上不携带槽位
// 信息（我们注入真实名就是为了界面显示，opus/sonnet 槽发同一个
// glm-5.3），一对多的反查没有真值，只能按「请求最可能从哪个槽发来」定
// 优先级：分类器与 compact 总结在 sonnet 槽（mid），是按发最密的；主循环
// 在 opus 槽（四档化后 = normal）次之；haiku 槽（light）是起标题这类小活；
// fable 槽（heavy）现在只剩明确点名。同名填多档（normal 和 mid 都绑
// glm-5.3 是常见配置）按 normal 先命中，会把大量 mid 请求硬说成 normal
// 体格（2026-09-09 实抓：分类器因此被 claude-bg 的档位闸门跳过，留在
// mid 体格的模型上跑 27 秒一条）。mid 优先让反查结果对齐请求的主流来源；
// 代价只是主循环的请求按 mid 算——它本就是流式，不吃档位闸门。
var realNameRoleOrder = []string{"mid", "normal", "light", "heavy", "vision"}

// ResolveRequest 把客户端发来的 model 字段解析成一条 fallback 链。
//
// 两种形态（docs/18 §5）：
//
//   - 角色名（"heavy"、"newgate/heavy"、"heavy[1m]" 归一化后；也包括模块贡献
//     的动态角色如 "omo-sisyphus"）→ 走该角色的链，与旧行为一致；
//   - 具体模型名（"deepseek-chat"）→ 找出哪个档位的链里有这个模型，把它挪到
//     链头（客户端点名要它，就先试它），其余按该档位的链跟随。
//
// 具体模型这一支是「让工具界面显示真实模型名」成立的关键：wrapper 把真实
// 模型名写进工具的槽位 env，工具原样发回来，我们把它反解回它所属的档位。
// 同名填多档时按 realNameRoleOrder 的优先级取档（见其注释）——反查没有
// 真值，别把这里的返回当客户端意图用。
//
// 返回 tier == "" 表示这个模型既不是档位名，也不在任何档位的绑定里。
func ResolveRequest(model string, active string, profiles []*domain.Profile,
	provs *domain.Providers, o Opts) ([]Step, []Skip, string) {

	// 档位名或模块贡献的动态角色键（omo-sisyphus 这种）——都是「角色」，
	// 走同一条建链路径。判断不了是不是角色时才算具体模型名（见下）。
	if domain.IsKnownRole(model) {
		steps, skips := BuildChain(model, profiles, provs, o)
		return steps, skips, model
	}

	for _, role := range realNameRoleOrder {
		steps, skips := BuildChain(role, profiles, provs, o)
		if idx := indexOfModel(steps, model); idx >= 0 {
			// 点名模型放最前，其余保持原顺序（去重已由 BuildChain 保证）。
			reordered := make([]Step, 0, len(steps))
			reordered = append(reordered, steps[idx])
			reordered = append(reordered, steps[:idx]...)
			reordered = append(reordered, steps[idx+1:]...)
			return reordered, skips, role
		}
	}
	return nil, nil, ""
}

func indexOfModel(steps []Step, model string) int {
	for i, s := range steps {
		if s.Binding.Model == model {
			return i
		}
	}
	return -1
}
