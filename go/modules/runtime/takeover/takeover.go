// Package takeover 是「接管」这件事的唯一实现。
//
// 为什么要有这一层
//
// 接管一个 agent 有两种机制：给 claude 这类读环境变量的工具装 PATH shim，
// 给 opencode 这类读配置文件的工具改写它自己的配置。这是**实现细节**，
// 用户不该被迫知道——`newgate shim install claude` 这种命令把我们的内部
// 结构泄漏成了用户接口，用户还得先搞清楚自己的工具属于哪一类才会用。
//
// 所以对外只有两个动词：接管（On）和释放（Off）。用哪种机制由这一层按
// agent catalog capability 的注册信息决定（有 BaseURLEnv 的走 shim，
// 其余走配置文件改写）。CLI / TUI / Web 三个壳都只调这里，不各自实现
// 一遍——见 docs/03-architecture.md 与 docs/07-clients-runtime.md。
//
// 期望态与现实态
//
// 「用户想接管谁」记在 state.json（domain.State.Takeover），「现在实际接管了
// 谁」看磁盘。两者必须分开，否则会出现两种对称的故障：
//
//	stop 不摘 shim → `claude` 仍命中 shim，wrapper 把代理懒启动回来，
//	                 等于没停（用户报的原始 bug）
//	start 不装回去 → 起来了却不接管，claude 静默直连，用户以为在走 newgate
//
// 期望态让 start = 插上、stop = 拔掉，两个方向都不丢用户的意图。
package takeover

import (
	"fmt"
	"os"
	"sort"

	"github.com/rzbdz/newgate/go/modules/config/store"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
	"github.com/rzbdz/newgate/go/modules/runtime/injection"
)

// Mechanism 接管机制。用户不需要知道，但排查故障时要能看到。
type Mechanism string

const (
	// MechShim PATH 里放一个指向 newgate 的链接，在子进程里注入环境变量。
	// 用于 claude 这类读环境变量的工具：shell 里 export 的变量优先级高于
	// 配置文件，改配置文件会被静默盖掉。
	MechShim Mechanism = "shim"
	// MechConfig 改写工具自己的配置文件。
	MechConfig Mechanism = "config"
)

// Status 一个 agent 的接管状态。
type Status struct {
	Agent     string
	Mechanism Mechanism
	Wanted    bool     // 用户意愿（state.json）
	Active    bool     // 现实态（磁盘）
	Detail    string   // 一句人话：怎么接管的，或为什么没有
	Files     []string // config 机制涉及的文件
}

// Result 一次接管/释放的结果。
type Result struct {
	Agent     string
	Mechanism Mechanism
	// Skipped 这个 agent 被有意跳过了（用户 off 过它）。不是成功也不是失败，
	// 展示时必须和「接管成功」区分开——打成 ✓ 会让用户以为接管上了。
	Skipped bool
	Lines   []string // 逐条说明，直接展示给用户
	Warn    []string
	Err     error
}

func mechanismOf(a *agentapi.Agent) Mechanism {
	if a.Config != nil {
		return MechConfig
	}
	if a.BaseURLEnv != "" {
		return MechShim
	}
	return ""
}

// Agents 已知 agent，稳定顺序。
func Agents() []string {
	ids := agentstate.Catalog().Names()
	sort.Strings(ids)
	return ids
}

// List 每个 agent 的接管状态。
func List() []Status {
	agents := agentstate.Catalog()
	st := store.LoadState()
	var out []Status
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		s := Status{Agent: id, Mechanism: mechanismOf(a), Wanted: st.TakeoverWanted(id)}
		switch s.Mechanism {
		case MechShim:
			for _, n := range injection.Installed() {
				if n == id {
					s.Active = true
				}
			}
			if s.Active {
				s.Detail = fmt.Sprintf("%s/%s → newgate", injection.Dir(), id)
				if !injection.InPath() {
					s.Detail += "（但 shim 目录不在当前 shell 的 PATH 里，重开 shell）"
				}
			} else {
				s.Detail = "直连"
			}
		case MechConfig:
			for _, t := range a.Config.Targets() {
				if _, err := os.Stat(t); err != nil {
					continue
				}
				s.Files = append(s.Files, t)
				if a.Config.IsTakenOver(t) {
					s.Active = true
				}
			}
			if s.Active {
				s.Detail = "配置文件已改写 → 本地代理"
			} else if len(s.Files) == 0 {
				s.Detail = "没找到它的配置文件（没装？）"
			} else {
				s.Detail = "直连"
			}
		}
		out = append(out, s)
	}
	return out
}