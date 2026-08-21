package special

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/internal/gateway/rewrite"
)

func init() { Register(deepseek{}) }

// deepseek 修 DeepSeek（含各类转发 DeepSeek 的网关）在思考模式下的 400。
//
// 现场报错
//
//	API Error: 400 The `reasoning_content` in the thinking mode must be
//	passed back to the API. (request id: 2026082106362979332…)
//
// 成因：DeepSeek 的思考模型把 reasoning_content 当**对话状态的一部分**。
// 它在上一轮回给客户端的 assistant 消息里带了 reasoning_content，下一轮就
// 要求你把它原样回传。而 Claude Code 这类客户端根本不知道有这个字段——
// Anthropic 协议里没有——于是从第二轮起（尤其是带 tool_use 的多轮）必然缺失，
// 每次都 400。上游不放过这一点，客户端又不可能知道，只能由中间层补。
//
// 两手一起上，对应上游的两种实现口味：
//
//  1. 顶层 thinking:{"type":"disabled"} —— 直接关掉思考模式，从根上不需要
//     reasoning_content。这是 Anthropic 协议里合法的字段，且 disabled 就是
//     默认值，所以对不认识它的上游是语义无操作；DeepSeek 自家的
//     /chat/completions 也接受它（社区的 Node 临时代理就是这么绕过去的）。
//  2. 给 messages 里的 assistant 消息补 reasoning_content:"" —— 有些网关不
//     认 thinking 开关，只认「你把字段带回来了」。空串是合法的「没有思考
//     内容」，补上不改变任何语义。
//
// 上游 issue 的说法是「给带 tool call 的 assistant 消息补」，这里补的是
// **所有** assistant 消息：那是一个超集，代价是几个空字符串，换来的是不用
// 去猜每家网关怎么判断「这轮用了思考模式」。已经带了该字段的消息一律不碰。
//
// 摘除条件：哪天 DeepSeek 允许缺省 reasoning_content 了，`newgate st off
// deepseek` 就能验证；确认不需要了整个文件可以直接删掉。
type deepseek struct{}

func (deepseek) Name() string { return "deepseek" }

func (deepseek) Why() string {
	return "DeepSeek 思考模式要求回传 reasoning_content，Claude 侧没这字段 → 400\n" +
		"关掉思考模式，并给 assistant 消息补空 reasoning_content"
}

// Match 只认 DeepSeek：模型名、provider 名、base URL 任一处出现 deepseek。
//
// 为什么看这三处：模型名最可靠（deepseek-chat / deepseek-reasoner），但经过
// 聚合网关时可能被改名，此时 provider 名或 endpoint 里通常仍留着痕迹。
//
// 反向兜底：Anthropic 官方端点一律不碰。它对未知字段是严格的，而它也从来
// 不会报这个错——真有人在官方端点上挂了个叫 deepseek 的 provider，也不该
// 让这个补丁去给它加字段。
func (deepseek) Match(r *Request) bool {
	if r == nil {
		return false
	}
	if strings.Contains(strings.ToLower(r.BaseURL), "api.anthropic.com") {
		return false
	}
	for _, s := range []string{r.Model, r.Provider, r.BaseURL} {
		if strings.Contains(strings.ToLower(s), "deepseek") {
			return true
		}
	}
	return false
}

func (deepseek) Apply(body []byte, r *Request) ([]byte, []string, error) {
	var notes []string
	out := body

	// 1) 关掉思考模式。客户端自己写了 thinking 就尊重它——用户显式要
	//    思考模式时，我们不该悄悄关掉。
	if _, has := rewrite.TopLevelRaw(out, "thinking"); !has {
		nb, err := rewrite.InsertTopLevelRaw(out, "thinking", []byte(`{"type":"disabled"}`))
		if err != nil {
			return nil, nil, fmt.Errorf(`注入 thinking 失败: %w`, err)
		}
		out = nb
		notes = append(notes, `注入 thinking:{"type":"disabled"}`)
	}

	// 2) 给 assistant 消息补 reasoning_content:""。
	//    没有 messages（如 /v1/models 之类）就什么也不做，不算错。
	if _, has := rewrite.TopLevelRaw(out, "messages"); has {
		nb, n, err := rewrite.EnsureArrayItemField(out, "messages",
			"reasoning_content", []byte(`""`), isAssistant)
		switch {
		case err != nil:
			// messages 形状不认识：前面那半仍然有效，这半放弃。
			notes = append(notes, "messages 未改动（"+err.Error()+"）")
		case n > 0:
			out = nb
			notes = append(notes, fmt.Sprintf(`给 %d 条 assistant 消息补 reasoning_content:""`, n))
		}
	}

	return out, notes, nil
}

func isAssistant(item []byte) bool {
	role, _ := rewrite.TopLevelString(item, "role")
	return role == "assistant"
}
