package special

import (
	"bytes"

	"github.com/rzbdz/newgate/go/internal/gateway/rewrite"
)

func init() { Register(claudeBg{}) }

// claudeBg 修 Claude Code 后台小请求撞上「默认思考」模型后的慢。
//
// 现场实抓（2026-09，Claude Code 2.1.263，NEWGATE_DUMP 落盘复核）：
//
//	主循环     heavy + stream=true + tools:33 + thinking:adaptive
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
// 两手，各管一层：
//
//  1. 切 light（只对分类器本体）：判定就用上面实抓的 system 标记，不猜。
//     「这条命令安全不安全」不需要 mid 的体格——light（glm-4.5-air）
//     实测接受 thinking:disabled，200KB 输入 1-3s。其他后台调用（compact
//     总结、起标题）**保留 mid**：它们要一点质量，且不挡交互。标记对不上
//     时自然降级：分类器留在 mid + 禁思考，慢一点但不卡死。
//  2. 禁思考（对全部后台调用）：一律显式 thinking:{"type":"disabled"}，
//     缺就补，带了也改写（后台调用里带的 thinking 是客户端设置泄漏过去
//     的，不是这次调用真要推理）。撞上「该模型始终思考」的 400（GLM
//     1210）时由排最后的 always-thinks 兜底（改回 enabled +
//     reasoning_effort:low）——本插件只表达「客户端这次不想思考」的意图，
//     翻译成各家上游听得懂的话是模型级插件的事。
//
// 特征为什么可靠：主循环**永远是流式的**（dump 佐证），非流式的只剩后台
// 小调用；档位上它们落在 mid/light——点名 heavy 的非流式调用是真要大模型
// 干活的，不碰。
//
// 这是少数会**改写客户端显式意图**的补丁，所以三条保险：
//   - notes 明说改了什么（不静默，docs/16）；
//   - `newgate st off claude-bg` 一键摘除；
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
		"分类器（system 自报 \"security monitor\"）切 light 档，其余后台调用只禁思考" +
		"（主循环的流式请求不受影响）"
}

// Match 认「Claude Code 的后台小调用」这个类：claude 发起 + 非流式 +
// mid/light 档。分类器本体的精确判定在 Apply 里看 system 标记。
func (claudeBg) Match(r *Request) bool {
	return r != nil && r.Agent == "claude" && !r.Stream &&
		(r.Tier == "mid" || r.Tier == "light")
}

// Apply 每一步都独立 fail-open：切不成模型就只禁思考，禁不成思考就只切
// 模型——多补一手是一手，绝不因为一个字段改不动就整个放弃。
func (claudeBg) Apply(body []byte, r *Request) ([]byte, []string, error) {
	const why = "后台小调用要快不要思考"
	var notes []string
	out := body

	// 1) 分类器本体 → 切 light。只在同一 provider 时切——跨 provider 要换
	//    上游和 key，那是路由的事，body 改写管不着。fallback 链换到别家时
	//    会在这里自然停手（Provider 对不上）。
	if r.LightModel != "" && r.LightProvider == r.Provider && r.LightModel != r.Model &&
		isClassifier(out) {
		if nb, err := rewrite.ReplaceTopLevelString(out, "model", r.LightModel); err == nil {
			out = nb
			// 上下文（r.Model）不用手动回写：Apply 框架每步后从 body 同步，
			// 排在后面的插件自然看到切换后的模型。
			notes = append(notes, "模型 "+r.Model+" → "+r.LightModel+"（Bash 分类器用轻档跑）")
		} else {
			notes = append(notes, "模型未切换（"+err.Error()+"）")
		}
	}

	// 2) 禁思考。reasoning_effort 与 thinking:disabled 互斥（见 st-deepseek
	//    同款注释）：客户端设了推理强度就别去关它。
	if _, effort := rewrite.TopLevelRaw(out, "reasoning_effort"); effort {
		return out, notes, nil
	}
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); has {
		if t, _ := rewrite.TopLevelString(raw, "type"); t == "disabled" {
			return out, notes, nil // 已经是关的
		}
		nb, err := rewrite.ReplaceTopLevelRaw(out, "thinking", []byte(`{"type":"disabled"}`))
		if err != nil {
			notes = append(notes, "thinking 未改动（"+err.Error()+"）")
			return out, notes, nil
		}
		return nb, append(notes, `thinking 已改写为 {"type":"disabled"}（`+why+`）`), nil
	}
	nb, err := rewrite.InsertTopLevelRaw(out, "thinking", []byte(`{"type":"disabled"}`))
	if err != nil {
		notes = append(notes, "thinking 未注入（"+err.Error()+"）")
		return out, notes, nil
	}
	return nb, append(notes, `注入 thinking:{"type":"disabled"}（`+why+`）`), nil
}
