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
// agent 的注册信息决定（agents.Registry：有 BaseURLEnv 的走 shim，
// 其余走配置文件改写）。CLI / TUI / Web 三个壳都只调这里，不各自实现
// 一遍——docs/02 §2、docs/12 §1。
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

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"github.com/rzbdz/newgate/go/internal/store"
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

func mechanismOf(a *agents.Agent) Mechanism {
	if a.BaseURLEnv != "" {
		return MechShim
	}
	return MechConfig
}

// Agents 已知 agent，稳定顺序。
func Agents() []string {
	ids := agents.Names()
	sort.Strings(ids)
	return ids
}

// List 每个 agent 的接管状态。
func List() []Status {
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
			for _, t := range paths.TargetFiles() {
				if _, err := os.Stat(t); err != nil {
					continue
				}
				s.Files = append(s.Files, t)
				if injection.IsTakenOver(t) {
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

// On 接管一个 agent，并记下「用户要接管它」。
func On(agent string, port int) Result {
	a, ok := agents.Get(agent)
	if !ok {
		return Result{Agent: agent, Err: unknown(agent)}
	}
	if err := store.SetTakeoverWanted(agent, true); err != nil {
		return Result{Agent: agent, Mechanism: mechanismOf(a), Err: err}
	}
	return apply(a, port)
}

// Off 释放一个 agent，并记下「用户不要接管它」——之后 start 也不会再接管它。
func Off(agent string) Result {
	a, ok := agents.Get(agent)
	if !ok {
		return Result{Agent: agent, Err: unknown(agent)}
	}
	if err := store.SetTakeoverWanted(agent, false); err != nil {
		return Result{Agent: agent, Mechanism: mechanismOf(a), Err: err}
	}
	return release(a)
}

// OnAll 全面接管：所有没被用户显式关掉的 agent。不改任何意愿。
func OnAll(port int) []Result {
	st := store.LoadState()
	var out []Result
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		if !st.TakeoverWanted(id) {
			out = append(out, Result{Agent: id, Mechanism: mechanismOf(a), Skipped: true,
				Lines: []string{"跳过（你 off 过它；newgate on " + id + " 可恢复）"}})
			continue
		}
		out = append(out, apply(a, port))
	}
	return out
}

// OffAll 全面释放。**不改意愿**——这样 newgate stop 之后再 start，
// 用户原本接管的那些还会自动插回去。
func OffAll() []Result {
	var out []Result
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		r := release(a)
		if len(r.Lines) == 0 && r.Err == nil {
			continue // 本来就没接管，不用报
		}
		out = append(out, r)
	}
	return out
}

func apply(a *agents.Agent, port int) Result {
	res := Result{Agent: a.ID, Mechanism: mechanismOf(a)}
	switch res.Mechanism {
	case MechShim:
		link, err := injection.Install(a.ID)
		if err != nil {
			res.Err = err
			return res
		}
		res.Lines = append(res.Lines, "PATH shim "+link+" → newgate")
		for _, rc := range injection.RCFiles() {
			changed, err := injection.AddToRC(rc)
			if err != nil {
				res.Warn = append(res.Warn, rc+": "+err.Error())
				continue
			}
			if changed {
				res.Lines = append(res.Lines, "把 "+injection.Dir()+" 前置到 PATH（写进 "+rc+"）")
			}
		}
		if real, err := a.FindReal(injection.Dir()); err == nil {
			res.Lines = append(res.Lines, "真实 "+a.ID+": "+real)
		} else {
			res.Warn = append(res.Warn, "找不到真实的 "+a.ID+"："+err.Error()+
				"——shim 会转发失败，先把它装好")
		}
		if !injection.InPath() {
			res.Warn = append(res.Warn, "当前 shell 的 PATH 还没生效，重开 shell 或 `exec $SHELL -l`")
		}

	case MechConfig:
		reps, err := injection.ApplyAll(port)
		if err != nil {
			res.Err = err
			return res
		}
		for _, rep := range reps {
			if rep.Skipped != "" {
				res.Lines = append(res.Lines, rep.File+"（跳过: "+rep.Skipped+"）")
				continue
			}
			line := rep.File
			if n := len(rep.Rewrites); n > 0 {
				line += fmt.Sprintf("（%d 处改写）", n)
			}
			res.Lines = append(res.Lines, line)
			if rep.Fuzzy > 0 {
				res.Warn = append(res.Warn, fmt.Sprintf(
					"%s 里有 %d 处没命中精确规则，归到了中档，建议核对", rep.File, rep.Fuzzy))
			}
		}
	}
	return res
}

func release(a *agents.Agent) Result {
	res := Result{Agent: a.ID, Mechanism: mechanismOf(a)}
	switch res.Mechanism {
	case MechShim:
		installed := false
		for _, n := range injection.Installed() {
			if n == a.ID {
				installed = true
			}
		}
		if !installed {
			return res
		}
		if err := injection.Uninstall(a.ID); err != nil {
			res.Err = err
			return res
		}
		res.Lines = append(res.Lines, fmt.Sprintf(
			"摘掉 PATH shim（PATH 优先级回到真实 %s）", a.ID))

	case MechConfig:
		restored, err := injection.RestoreAll()
		if err != nil {
			res.Err = err
			return res
		}
		for _, f := range restored {
			res.Lines = append(res.Lines, "还原 "+f)
		}
	}
	return res
}

func unknown(agent string) error {
	return fmt.Errorf("不认识的 agent %q（已知：%v）", agent, Agents())
}
