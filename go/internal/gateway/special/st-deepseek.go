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
// 补什么内容，三级来源，逐级降级：
//
//  1. 客户端自己带回的 thinking 块原文——它保留了就直接用，不依赖缓存；
//  2. gateway/thinkcache 里那一轮**真实**的推理内容；
//  3. 都没有 → 占位符（reasoningFallback）。
//
// 占位符必须**非空**：空串虽然能骗过大多数轮次，但 2026-09 在 new-api 直连
// DeepSeek 官方接口的部署上实抓到——同一份「补空」请求，有的轮次 400
// （"must be passed back" 连空串也不放过）有的轮次 200（官方侧多节点/灰度）。
// 所以按最严的口径来：宁可占位也不能空。占位内容明说「推理没被保留」，
// 模型至少知道那一轮它想过、只是原文丢了，比读到自己「什么都没想」强。
//
// 补的范围是**所有** assistant 消息，而不只是带 tool call 的那些。这不是
// 保守，是文档的要求：请求一旦带了 tools，历史里每条 assistant 消息都得带
// 推理内容，哪怕那一轮没做 tool call。已经带了思考内容的消息一律不碰。
//
// 摘除条件：哪天 DeepSeek 允许缺省这些字段了，`newgate st off deepseek`
// 就能验证；确认不需要了整个文件可以直接删掉。
type deepseek struct{}

// reasoningFallback 缓存查不到、客户端也没带回思考块时的占位内容。
// 见上面文件头注释——必须非空，空串在最严的官方检查下同样 400。
const reasoningFallback = "（这一轮的推理内容没有被保留，无法回传原文。）"

func (deepseek) Name() string { return "deepseek" }

func (deepseek) Why() string {
	return "DeepSeek 思考模式要求逐字回传推理内容，客户端却会把它剥掉 → 400\n" +
		"没开思考就显式关掉；开着就补回去（客户端带回的原文 → thinkcache → 非空占位）"
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
	// 认不出 type（adaptive、或形状不认识）时按「开着」处理：多补两个占位块
	// 顶多是冗余，漏补就是 400。

	// 没有 messages（如 /v1/models 之类）就到此为止，不算错。
	if _, has := rewrite.TopLevelRaw(out, "messages"); !has {
		return out, notes, nil
	}

	// 2) 给 assistant 消息补 reasoning_content（OpenAI 方言那句报错）。
	//    真实原文 → 占位符，绝不空串。
	restored, placeholders := 0, 0
	valReasoning := func(item []byte) []byte {
		if q, ok := pickReasoning(item); ok {
			restored++
			return q
		}
		placeholders++
		q, _ := json.Marshal(reasoningFallback)
		return q
	}
	if nb, n, err := rewrite.EnsureArrayItemFieldFunc(out, "messages",
		"reasoning_content", valReasoning, isAssistant); err != nil {
		// messages 形状不认识：前面那些仍然有效，这步放弃。
		notes = append(notes, "messages 未改动（"+err.Error()+"）")
	} else if n > 0 {
		out = nb
		notes = append(notes, reasoningNote("reasoning_content", n, restored, placeholders))
	}

	// 3) 思考模式开着 → assistant 的 content[] 开头必须有 thinking 块
	//    （Anthropic 方言那句报错）。同样：真实原文 → 占位符。
	if thinkingOn {
		restored, placeholders = 0, 0
		valBlock := func(item []byte) []byte {
			text := reasoningFallback
			// 消息自带 reasoning_content（OpenAI 方言客户端保住了它）就
			// 用它，和第 2 步对同一份内容的来源保持一致
			if rc, ok := rewrite.TopLevelString(item, "reasoning_content"); ok && rc != "" {
				text = rc
				restored++
			} else if blob, ok := thinkcache.Default.Lookup(item); ok {
				text = string(blob)
				restored++
			} else {
				placeholders++
			}
			q, _ := json.Marshal(text) // string 编码不会失败
			return []byte(`{"type":"thinking","thinking":` + string(q) + `}`)
		}
		if nb, n, err := rewrite.EnsureArrayItemArrayHeadFunc(out, "messages", "content",
			valBlock, isAssistant, lacksThinking); err != nil {
			notes = append(notes, "content 未改动（"+err.Error()+"）")
		} else if n > 0 {
			out = nb
			notes = append(notes, reasoningNote("thinking 块", n, restored, placeholders))
		}
	}

	return out, notes, nil
}

// pickReasoning 为一条 assistant 消息选出回传的推理原文（JSON 编码后的字符串值）。
//
// 两级来源，严格对应客户端回传的形态：
//  1. 消息自己的 content[] 里带的 thinking 块——客户端保留了原文就直接用，
//     不依赖缓存存活；
//  2. thinkcache 里那轮真实发生的推理（tool id / 正文哈希找回）。
//
// 都没有 → false，调用方补非空占位符。
func pickReasoning(item []byte) ([]byte, bool) {
	if t := clientThinkingText(item); t != "" {
		if q, err := json.Marshal(t); err == nil {
			return q, true
		}
	}
	if blob, ok := thinkcache.Default.Lookup(item); ok {
		if q, err := json.Marshal(string(blob)); err == nil {
			return q, true
		}
	}
	return nil, false
}

// clientThinkingText 抽出这条消息 content[] 里 thinking 块的文本。
//
// 客户端（开了 interleaved thinking 的 Claude Code）有时会把自己那轮的
// thinking 块原样带回来——那是推理原文，别浪费。多个块就拼起来。
// redacted_thinking 没有 thinking 字段，自然取不到文本。
func clientThinkingText(item []byte) string {
	content, ok := rewrite.TopLevelRaw(item, "content")
	if !ok || len(content) == 0 || content[0] != '[' {
		return ""
	}
	blocks, ok := rewrite.ArrayItems(content)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if len(b) == 0 || b[0] != '{' {
			continue
		}
		if t, _ := rewrite.TopLevelString(b, "type"); t == "thinking" {
			if s, ok := rewrite.TopLevelString(b, "thinking"); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

// reasoningNote 把「补回真实原文的」和「只能占位的」分开报。
//
// 必须分开：占位意味着模型这一轮读不到自己上一轮的推理，思考质量会掉。
// 这不是成功，用户有权在日志里一眼看出发生了多少次（docs/16 的「不静默」）。
func reasoningNote(what string, total, restored, placeholders int) string {
	s := fmt.Sprintf("给 %d 条 assistant 消息补 %s：%d 条用了真实的推理原文",
		total, what, restored)
	if placeholders > 0 {
		s += fmt.Sprintf("，%d 条只能补占位符（缓存里没有、客户端也没带回思考块——"+
			"这几轮模型看不到自己的推理）", placeholders)
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
