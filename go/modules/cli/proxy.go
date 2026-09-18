package cli

import (
	breakerstatus "github.com/rzbdz/newgate/go/modules/breaker/status"
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"
)

// proxyInfo 是控制面状态文档的别名。
//
// 文档形状与客户端都在 modules/gateway/controlplane（共享叶子）：读守护进程的
// 自报状态不再是界面独有的能力，任何模块都能读——那正是把 metrics / breaker /
// probe 这些命令搬回各自模块的前提。
type proxyInfo = controlplane.Doc

// availableFromProxy 把 daemon 的全局熔断表冻结成一次 CLI 命令内的一致快照。
// daemon 不在线时不凭空判坏：诊断退化为只看静态配置。
func availableFromProxy(ps *proxyInfo) func(provider, model string) bool {
	blocked := map[string]bool{}
	if ps != nil {
		for _, b := range ps.Breakers {
			if b.Open {
				blocked[b.Provider+"/"+b.Model] = true
			}
		}
	}
	return func(provider, model string) bool {
		return !blocked[provider+"/"+model]
	}
}

// rankFromProxy 把 daemon 算好的排序键原样递给诊断。
//
// **这里不重算阈值**：3000ms / 12000ms 那套分桶只存在于 modules/breaker，
// CLI 抄一份的话 daemon 改阈值 CLI 不会跟着变（2026-09-17 之前就是这样，
// 两边各有一份 3000/12000）。排序策略只有一个来源。
//
// 老 daemon 不发 `rank`（优雅交接期间 CLI 与 daemon 可以来自不同版本），读不到
// 就退化成中性值——排序退化为「按配置顺序」，不会因为版本不齐而互相打架。
// 被摘牌的 binding 不在这里沉底：建链期先问 Available，被摘的根本进不了候选。
func rankFromProxy(ps *proxyInfo) func(provider, model string) int {
	const neutral = 1_000_000
	scores := map[string]int{}
	if ps != nil {
		for _, b := range ps.Breakers {
			score := neutral
			if b.Rank != 0 {
				score = b.Rank
			}
			scores[b.Provider+"/"+b.Model] = score
		}
	}
	return func(provider, model string) int {
		if score, ok := scores[provider+"/"+model]; ok {
			return score
		}
		return neutral
	}
}

func healthFromProxy(ps *proxyInfo) map[string]breakerstatus.Status {
	out := map[string]breakerstatus.Status{}
	if ps != nil {
		for _, status := range ps.Breakers {
			out[status.Provider+"/"+status.Model] = status
		}
	}
	return out
}

// proxyState 代理的进程信息 + 自报状态。薄薄一层转发，调用点不必改。
func proxyState() (*controlplane.Info, *proxyInfo) { return controlplane.State() }

// pingProxy 端口上的控制面活着吗。
func pingProxy(port int) bool { return controlplane.Ping(port) }

// notifyProxy 让运行中的代理立刻重读配置（转发到控制面叶子）。
//
// 为什么这一步归界面：命令改完配置要**立刻**生效，而界面是发起改动的那一方
// （见 Host.NotifyProxy）。
func notifyProxy() { controlplane.Notify() }
