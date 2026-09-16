package special

import (
	"bytes"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/gateway/rewrite"
)

func init() { Register(claudeBg{}) }

// claudeBg 修 Claude Code 后台小请求撞上「默认思考」模型后的慢。
//
// 现场实抓（2026-09，Claude Code 2.1.263，NEWGATE_DUMP 落盘复核）：
//
//	主循环     normal + stream=true + tools:33 + thinking:adaptive
//	后台调用   mid   + 非流式      + tools 无 + thinking 没写
//	分类器本体 再叠加：system ~126KB，开头是
//	           "You are a security monitor for autonomous AI coding agents"；
//	           2 条 user 消息（CLAUDE.md + 完整会话 transcript）；
//	           stop_sequences ["</block>"]；max_tokens 2112
//
// 而 glm-5.3 这类国模**默认开思考**，「没写」不等于「关了」，于是分类器
// 15-30 秒才回、成波超时重试（日志里一串「客户端在连接阶段就取消」），
// 整个开发会话跟着卡死。
//
// 分两层，各管一件事：
//
//  1. 改道（Route，路由层）：分类器本体整个走 **light 链**——不只是
//     链头换 light，fallback 也在 light 链里走。「这条命令安全不安全」要
//     的是快和便宜，这个意图必须贯穿整条链：只换头的话，light 一挂掉回
//     mid 的体格，又慢回去了。其他后台调用（compact 总结、起标题）不改
//     道：它们要一点质量，且不挡交互；标记对不上时也自然不改道。
//  2. 禁思考（Apply，body 层）：对「没写 thinking 的」后台调用补显式
//     thinking:{"type":"disabled"}，写了的不碰（compact 总结显式带
//     adaptive，强改 disabled 会被始终思考模型拒掉——见 BestEffortDisableThink）。
//     撞上「该模型始终思考」的 400（GLM 1210）时由排最后的 always-thinks
//     兜底（改回 enabled + reasoning_effort:low）——本插件只表达「客户端
//     这次不想思考」的意图，翻译成各家上游听得懂的话是模型级插件的事。
//
// 特征为什么可靠：主循环**永远是流式的**（dump 佐证，含 -p 模式），非流式
// 的只剩后台小调用；后台调用里分类器本体再靠 system 标记精确认出。
// 两层都不看 Tier——真实模型名注入后 Tier 是「名字反查的猜测」（见
// resolve.realNameRoleOrder），标记和非流式才是精确特征。
//
// 这是少数会**改写客户端显式意图**的补丁，所以三条保险：
//   - notes / 日志明说改了什么（不静默，docs/16）；
//   - `newgate st off claude-bg` 一键摘除（改道和禁思考一起停）；
//   - 只认 Agent=="claude"（opencode 等其他客户端不受影响）。
type claudeBg struct{}

// classifierMarker 分类器系统提示词的开头（cc 2.1.263 实抓，4/4 命中）。
// 出现在 system 里 = 这条非流式请求是 Bash 安全分类器本体。
const classifierMarker = "You are a security monitor"

// isClassifier 用实抓特征认分类器：只看 system 里有没有那句自报家门。
func isClassifier(body []byte) bool {
	raw, ok := rewrite.TopLevelRaw(body, "system")
	return ok && bytes.Contains(raw, []byte(classifierMarker))
}

func (claudeBg) Name() string { return "claude-bg" }

func (claudeBg) Why() string {
	return "Claude Code 的后台非流式请求（Bash 分类器等）不带 thinking，" +
		"国模却默认思考 → 15-30 秒、成波超时卡死会话\n" +
		"分类器（system 自报 \"security monitor\"）整条链走 light 档" +
		"（含 fallback），其余后台调用只禁思考" +
		"（主循环的流式请求不受影响）"
}

func (claudeBg) Route(body []byte, request *Request, state *domain.State) (RouteDecision, bool) {
	if request == nil || request.Agent != "claude" || request.Stream || !isClassifier(body) {
		return RouteDecision{}, false
	}
	decision := RouteDecision{
		Tier:             "light",
		FirstByteTimeout: state.Timeouts.ClassifierFirstByte(),
		Note:             "分类器改道 → light 档链（含 fallback）",
		Metric:           "route_light",
	}
	if override := state.ClassifierOverride; override != nil &&
		override.Provider != "" && override.Model != "" {
		head := *override
		decision.Head = &head
		decision.OverrideNote = "分类器覆盖 → " + head.String() +
			"（全局最高优先，先于任何 profile）"
		decision.OverrideFailNote = "分类器覆盖 " + head.String() +
			" 未生效，回落 light 档链"
	}
	return decision, true
}

func (claudeBg) Status(state *domain.State) []StatusItem {
	if state == nil {
		return nil
	}
	if override := state.ClassifierOverride; override != nil &&
		override.Provider != "" && override.Model != "" {
		return []StatusItem{{
			Label: "分类器覆盖",
			Value: override.String() + " · 全局最高优先，先于任何 profile",
		}}
	}
	return []StatusItem{{
		Label: "分类器改道",
		Value: "Claude Code Bash 分类器 → light 档链",
	}}
}

func (claudeBg) Bindings(state *domain.State) []domain.Binding {
	if state == nil {
		return nil
	}
	if override := state.ClassifierOverride; override != nil &&
		override.Provider != "" && override.Model != "" {
		return []domain.Binding{*override}
	}
	return nil
}

func (claudeBg) Metrics() []MetricInfo {
	return []MetricInfo{{
		Action: "route_light",
		Hint:   "Bash 分类器，整条链改走 light",
	}}
}

// Match 认「Claude Code 的后台小调用」这个类：claude 发起 + 非流式。
// 分类器本体的精确判定（system 标记）在 Route 里，那边管改道。
func (claudeBg) Match(r *Request) bool {
	return claudeCode(r) && !r.Stream
}

// Apply 认出后台调用后，把「这次调用不想思考」交给 BestEffortDisableThink
// ——意图在这里，翻译（模型不支持关思考时改成最小思考）在那边，best
// effort：关不掉就让它思考，绝不因此失败。改道没命中（Route 没认出
// 分类器）时同样只禁思考，慢而不死。
func (claudeBg) Apply(body []byte, r *Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, r)
}
