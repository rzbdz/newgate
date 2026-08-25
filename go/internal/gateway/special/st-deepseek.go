package special

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/internal/gateway/rewrite"
	"github.com/rzbdz/newgate/go/internal/gateway/thinkcache"
)

func init() { Register(deepseek{}) }

// deepseek 修 DeepSeek（含各类转发 DeepSeek 的网关）在思考模式下的 400。
//
// 现场报错，两种方言各一句：
//
//	API Error: 400 The `reasoning_content` in the thinking mode must be
//	passed back to the API.                          （OpenAI 方言端点）
//	API Error: 400 The `content[].thinking` in the thinking mode must be
//	passed back to the API.                          （Anthropic 方言端点）
//
// 成因是同一件事：DeepSeek 的思考模型把思考内容当**对话状态的一部分**。
// 它上一轮回给客户端的 assistant 消息里带了思考内容，下一轮就要求你原样
// 回传；一旦某轮出现过 tool call，之后每一轮都查。而 Claude Code 这类
// 客户端对非 anthropic.com 的端点会**主动把 thinking 块剥掉**（它假定只有
// 官方端点签名的 thinking 块能回传），于是从第二轮起必然缺失，每次都 400。
// 上游不放过，客户端不可能知道，只能中间层补。
//
// 三手一起上，对应上游的三种口味：
//
//  1. 客户端没写 thinking → 补顶层 thinking:{"type":"disabled"}，从根上不
//     进思考模式。这是 Anthropic 协议里的合法字段，disabled 也是默认值，
//     对不认识它的上游是语义无操作；而 DeepSeek 是**必须显式写**才算关，
//     省略不算。
//  2. messages 里的 assistant 消息补 reasoning_content（OpenAI 方言）。
//  3. 思考模式确实开着时（客户端自己写了 enabled/adaptive），给 assistant
//     消息的 content[] **开头**补 thinking 块（Anthropic 方言）。位置是协议
//     的一部分——thinking 必须排在 text / tool_use 之前。
//
// 第 3 手只在思考模式开着时做：思考关掉时再塞 thinking 块，反而会被上游
// 以「关了还给我思考块」拒掉。
//
// 补什么内容：优先补 gateway/thinkcache 里那一轮**真实**的推理内容。
// 文档写明必须逐字回传，截断或摘要同样被拒——而补空串虽然能骗过「字段在
// 不在」的检查，却让模型下一轮读到自己上一轮「什么都没想」，推理质量直接
// 塌掉。所以空串只是缓存查不到时的最后兜底，且必须在日志里说清楚有几条
// 是空的。
//
// 补的范围是**所有** assistant 消息，而不只是带 tool call 的那些。这不是
// 保守，是文档的要求：请求一旦带了 tools，历史里每条 assistant 消息都得带
// 推理内容，哪怕那一轮没做 tool call。已经带了思考内容的消息一律不碰。
//
// 摘除条件：哪天 DeepSeek 允许缺省这些字段了，`newgate st off deepseek`
// 就能验证；确认不需要了整个文件可以直接删掉。
type deepseek struct{}

func (deepseek) Name() string { return "deepseek" }

func (deepseek) Why() string {
	return "DeepSeek 思考模式要求逐字回传推理内容，客户端却会把它剥掉 → 400\n" +
		"没开思考就显式关掉；开着就用 thinkcache 里缓存的原文补回去（查不到才补空）"
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

	// 1) 客户端没写 thinking 就显式关掉。写了就尊重它——用户显式要思考
	//    模式时我们不该悄悄关掉，改成走第 3 步把 thinking 块补回去。
	thinkingOn := true
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); !has {
		// reasoning_effort 与 thinking:disabled 互斥：上游会回
		// 「thinking options type cannot be disabled when reasoning_effort
		// is set」。客户端设了推理强度就是明确要思考，别去关它，
		// 直接走第 3 步补块。
		if _, effort := rewrite.TopLevelRaw(out, "reasoning_effort"); !effort {
			nb, err := rewrite.InsertTopLevelRaw(out, "thinking", []byte(`{"type":"disabled"}`))
			if err != nil {
				return nil, nil, fmt.Errorf(`注入 thinking 失败: %w`, err)
			}
			out = nb
			thinkingOn = false
			notes = append(notes, `注入 thinking:{"type":"disabled"}`)
		}
	} else if t, _ := rewrite.TopLevelString(raw, "type"); t == "disabled" {
		thinkingOn = false
	}
	// 认不出 type（adaptive、或形状不认识）时按「开着」处理：多补两个空块
	// 顶多是冗余，漏补就是 400。

	// 没有 messages（如 /v1/models 之类）就到此为止，不算错。
	if _, has := rewrite.TopLevelRaw(out, "messages"); !has {
		return out, notes, nil
	}

	// 2) 给 assistant 消息补 reasoning_content（OpenAI 方言那句报错）。
	//    优先用缓存里那一轮**真实**的推理内容；查不到才退回空串。
	restored, blanks := 0, 0
	valReasoning := func(item []byte) []byte {
		if blob, ok := thinkcache.Default.Lookup(item); ok {
			q, err := json.Marshal(string(blob))
			if err == nil {
				restored++
				return q
			}
		}
		blanks++
		return []byte(`""`)
	}
	if nb, n, err := rewrite.EnsureArrayItemFieldFunc(out, "messages",
		"reasoning_content", valReasoning, isAssistant); err != nil {
		// messages 形状不认识：前面那些仍然有效，这步放弃。
		notes = append(notes, "messages 未改动（"+err.Error()+"）")
	} else if n > 0 {
		out = nb
		notes = append(notes, reasoningNote("reasoning_content", n, restored, blanks))
	}

	// 3) 思考模式开着 → assistant 的 content[] 开头必须有 thinking 块
	//    （Anthropic 方言那句报错）。同样优先用缓存。
	if thinkingOn {
		restored, blanks = 0, 0
		valBlock := func(item []byte) []byte {
			text := ""
			if blob, ok := thinkcache.Default.Lookup(item); ok {
				text = string(blob)
			}
			q, err := json.Marshal(text)
			if err != nil {
				blanks++
				return []byte(`{"type":"thinking","thinking":""}`)
			}
			if text == "" {
				blanks++
			} else {
				restored++
			}
			return []byte(`{"type":"thinking","thinking":` + string(q) + `}`)
		}
		if nb, n, err := rewrite.EnsureArrayItemArrayHeadFunc(out, "messages", "content",
			valBlock, isAssistant, lacksThinking); err != nil {
			notes = append(notes, "content 未改动（"+err.Error()+"）")
		} else if n > 0 {
			out = nb
			notes = append(notes, reasoningNote("thinking 块", n, restored, blanks))
		}
	}

	return out, notes, nil
}

// reasoningNote 把「补回来的」和「只能补空的」分开报。
//
// 必须分开：补空串意味着模型这一轮读不到自己上一轮的推理，思考质量会掉。
// 这不是成功，用户有权在日志里一眼看出发生了多少次（docs/16 的「不静默」）。
func reasoningNote(what string, total, restored, blanks int) string {
	s := fmt.Sprintf("给 %d 条 assistant 消息补 %s：%d 条用了缓存里的原文",
		total, what, restored)
	if blanks > 0 {
		s += fmt.Sprintf("，%d 条只能补空（缓存里没有——重启过或超出保留期，"+
			"这几轮模型看不到自己的推理）", blanks)
	}
	return s
}

func isAssistant(item []byte) bool {
	role, _ := rewrite.TopLevelString(item, "role")
	return role == "assistant"
}

// lacksThinking 这条 content[] 里有没有思考内容。
// redacted_thinking 也算——那是上游自己加密过的思考块，有它就说明思考内容
// 已经原样回传了，再往前插一个空块只会多一个块。
func lacksThinking(content []byte) bool {
	items, ok := rewrite.ArrayItems(content)
	if !ok {
		return false // 形状不认识：不动
	}
	for _, it := range items {
		if len(it) == 0 || it[0] != '{' {
			continue
		}
		switch t, _ := rewrite.TopLevelString(it, "type"); t {
		case "thinking", "redacted_thinking":
			return false
		}
	}
	return true
}
