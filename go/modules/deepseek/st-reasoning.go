package deepseek

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
	"github.com/rzbdz/newgate/go/modules/gateway/thinkcache"
)

var (
	_ special.Plugin           = (*reasoning)(nil)
	_ special.ToolLoopMigrator = (*reasoning)(nil)
	_ special.MetricProvider   = (*reasoning)(nil)
	_ special.ResponseAuditor  = (*reasoning)(nil)
)

func Treatments() []special.Plugin { return []special.Plugin{reasoning{}} }

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
//  1. **只有 Claude Code** 没写 thinking 时 → 补顶层
//     thinking:{"type":"disabled"}，从根上不进思考模式。这是 Anthropic 协议
//     里的合法字段，disabled 也是默认值，对不认识它的上游是语义无操作；
//     而 DeepSeek 是**必须显式写**才算关，省略不算。
//  2. messages 里的 assistant 消息补 reasoning_content（OpenAI 方言）。
//  3. 思考模式确实开着、且这次是 Anthropic 方言时，给 assistant 消息的
//     content[] **开头**补 thinking 块（Anthropic 方言）。位置是协议的一部分
//     ——thinking 必须排在 text / tool_use 之前。
//
// 第 3 手只在思考模式开着时做：思考关掉时再塞 thinking 块，反而会被上游
// 以「关了还给我思考块」拒掉。
//
// 第 4 手（2026-09-17 新增，同日按实测重写判据）修的是**根因**，前几手都只是把
// 字段补齐：
//
//	**最后一条 user 消息**的 content[] 里全是 tool_result 块、一个字都没有时，
//	DeepSeek 的严格校验一律回「reasoning_content must be passed back」
//	——哪怕每一条历史消息的推理都逐字回了，哪怕上一条是 role:"system" 的插话。
//
// 这是实测排除法得出的结论，不是猜的：同一份真实 body（带完整历史）连发
// 多次都 400；把 reasoning_content 全换成真实文本、或全部删掉，结果都不变；
// 上游原文里点名的字段却明明是齐的。真正起作用的是尾部形状——追加一条普通
// 用户指令就 200（3/3，见 repairTailShape 的判据注释与 docs/06-reasoning.md
// §2b 的完整矩阵）。报错文案与真实原因不一致，是这个上游最坑的地方：它把
// 「你这轮没有新指令」也报成「推理没回传」。
//
// 第 1 手只给 Claude Code（`/a/claude/` 认出来，见 claudeCode）：它剥掉思考块，
// 思考开着也回不来，白花思考的时间和 token。**别的客户端不能关**——2026-09-15
// 现场：opencode 走 OpenAI 方言（`/chat/completions`），压根不会写 thinking 这个
// Anthropic 字段，于是条条请求都被当成「客户端没要思考」而关掉，用户报
// 「deepseek 不思考了」。opencode 要的是模型的默认思考，推理由第 2 手替它送回去。
//
// 第 3 手只对 Anthropic 方言做：OpenAI 方言里回传推理的载体是 reasoning_content
// （第 2 手），往人家的 content[] 里塞 thinking 块是塞一个它不认识的块类型。
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
type reasoning struct{}

// reasoningFallback 缓存查不到、客户端也没带回思考块时的占位内容。
// 见上面文件头注释——必须非空，空串在最严的官方检查下同样 400。
const reasoningFallback = "（这一轮的推理内容没有被保留，无法回传原文。）"

func (reasoning) Name() string { return "deepseek" }

func (reasoning) Why() string {
	return "DeepSeek 思考模式要求逐字回传推理内容，客户端却会把它剥掉 → 400\n" +
		"开着就补回去（客户端带回的原文 → thinkcache → 非空占位）；" +
		"只有 Claude Code 那条路干脆显式关掉思考（它剥块，想了也白想）；" +
		"外来未闭合 tool loop 先从可见工具结果有损重建，再由 DeepSeek 接手"
}

func (reasoning) Metrics() []special.MetricInfo {
	return []special.MetricInfo{{
		Action: "tool_loop_rebase",
		Hint:   "接手外来未闭合 tool loop 前做了有损重建",
	}}
}

// NeedsToolLoopRebase 标出 DeepSeek 接手别家未闭合 tool loop 时需要有损重建。
//
// 这不是通用限制。2026-09-16 对同一份真实 thinking + tool_use 做 A/B：
//   - Ark → Ark:      3/3 200
//   - Ark → DeepSeek: 3/3 400 reasoning_content must be passed back
//   - DeepSeek → Ark: 3/3 200
//   - DeepSeek → DS:  3/3 200
//
// API 是无状态的；不兼容的是请求里携带的 reasoning/tool 编码。只拦迁入
// DeepSeek，别把这个上游怪癖扩大成所有 provider 都失去 fallback。
func (d reasoning) NeedsToolLoopRebase(originProvider, originModel string,
	candidate *special.Request) (bool, string) {
	if !d.Match(candidate) ||
		(candidate.Provider == originProvider && candidate.Model == originModel) {
		return false, ""
	}
	return true, "接手其他上游未闭合的 reasoning/tool 状态前需要有损重建"
}

const toolLoopRebasePrompt = "Continue from the tool results above. " +
	"Re-evaluate them as a new step without relying on prior hidden reasoning."

// RebaseToolLoop 把 tool_result 变成同时带普通用户指令的新回合。旧 reasoning、
// tool_use、tool_result 全部保留作可见上下文，只追加这一段；实测 Ark →
// DeepSeek 原请求稳定 400，追加后 5/5 200。
func (reasoning) RebaseToolLoop(body []byte, _ *special.Request) ([]byte, string, error) {
	q, _ := json.Marshal(toolLoopRebasePrompt)
	block := []byte(`{"type":"text","text":` + string(q) + `}`)
	out, changed, err := rewrite.AppendLastArrayItemArray(body, "messages", "content",
		block, func(item []byte) bool {
			role, _ := rewrite.TopLevelString(item, "role")
			return role == "user"
		})
	if err != nil {
		return nil, "", err
	}
	if !changed {
		return body, "", nil
	}
	return out, "有损重建外来 tool loop：保留工具结果并追加普通用户继续指令", nil
}

// Match 只认 DeepSeek：模型名、provider 名、base URL 任一处出现 deepseek。
//
// 为什么看这三处：模型名最可靠（deepseek-chat / deepseek-reasoner），但经过
// 聚合网关时可能被改名，此时 provider 名或 endpoint 里通常仍留着痕迹。
//
// 反向兜底：Anthropic 官方端点一律不碰。它对未知字段是严格的，而它也从来
// 不会报这个错——真有人在官方端点上挂了个叫 deepseek 的 provider，也不该
// 让这个补丁去给它加字段。
func (reasoning) Match(r *special.Request) bool {
	return r != nil && MatchTarget(r.Model, r.Provider, r.BaseURL)
}

func (reasoning) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	var notes []string
	out := body

	// 是否开启思考只看请求的显式字段。Claude Code × DeepSeek 的缺省关闭
	// 由组合模块 claudecode_deepseek 负责，本模型模块不认识调用端。
	thinkingOn := true
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); has {
		if t, _ := rewrite.TopLevelString(raw, "type"); t == "disabled" {
			thinkingOn = false
		}
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

	// 4) 尾部形态：**最后一条 user 消息**的 content[] 里只有 tool_result、一个字
	//    都没有时，给它追加一条普通用户指令。详见文件头第 4 手。
	//
	//    顺序放在补字段之后：这一步改的是 messages 的尾部形状，前面两步改的是
	//    已有消息里的字段，互不影响；放在后面读起来也顺——先补全字段，再修形状。
	//
	//    **不能挂在 thinkingOn 上**。2026-09-17 实测（3/3，走 /p/ds 打真实上游，
	//    读 X-Newgate-Chain 上 deepseek 那一发的结论）：同一份 tool_result-only
	//    尾部，把顶层写成 thinking:{"type":"disabled"} 且**不带 tools**，
	//    上游照样回「reasoning_content must be passed back」。上游这条校验与
	//    思考开关无关——它只是把「你这轮没有新指令」也报成了推理缺失。
	if nb, changed, err := repairTailShape(out); err != nil {
		notes = append(notes, "尾部形状未改动（"+err.Error()+"）")
	} else if changed {
		out = nb
		notes = append(notes, "末尾的 user 轮只有 tool_result 没有用户指令——"+
			"追加一条继续指令（DeepSeek 对空指令尾部误报 reasoning_content 缺失）")
	}

	// 3) 思考模式开着 → assistant 的 content[] 开头必须有 thinking 块
	//    （Anthropic 方言那句报错）。同样：真实原文 → 占位符。
	//    只对 Anthropic 方言做：OpenAI 方言里回传推理的载体是 reasoning_content。
	if thinkingOn && anthropicDialect(r) {
		restored, placeholders = 0, 0
		valBlock := func(item []byte) []byte {
			// 与第 2 步同一份来源（pickReasoning）：客户端带回的 thinking 块
			// 原文 → thinkcache 真实推理 → 占位。**不能**读第 2 步刚补的
			// reasoning_content——第 2 步对没缓存的消息补的是占位符，读它会把
			// 占位符当成「真实原文」，让日志里的「用了真实原文」计数虚高。
			if q, ok := pickReasoning(item); ok {
				restored++
				return []byte(`{"type":"thinking","thinking":` + string(q) + `}`)
			}
			placeholders++
			q, _ := json.Marshal(reasoningFallback) // string 编码不会失败
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

// tailContinuation 是补在「只有 tool_result 的尾部」后面的那句用户指令。
//
// 措辞要像用户会说的话（模型会当成真实指令读）：明确「接着上面继续」，同时
// 把「要不要再调工具」的选择权留给模型——写死「给出最终答案」会让它在该继续
// 干活的时候停下来。
const tailContinuation = "Continue from the tool results above. " +
	"Call the next tool you need, or give your final answer."

// repairTailShape 修「最后一条 user 消息只有 tool_result」这个形状。
//
// 判据三条，每一条都是实测出来的（2026-09-17，3/3，打真实
// smt-deepseek/deepseek-flash，读 X-Newgate-Chain 上 deepseek 那一发的结论）：
//
//  1. **锚在「最后一条 role:user 的消息」，不是数组的最后一项**。Claude Code 会
//     在 tool_result 之后追加一条 role:"system" 的插话（「The user sent a new
//     message while you were working: …」），数组最后一项是那条 system，而被上游
//     拒掉的却是它前面那条只有 tool_result 的 user 轮。上游不认 system 里的指令，
//     所以那 400 一直存在。现场：dump/err-400-req000464（末尾 user[tool_result]
//     → system[text] → 400 must be passed back）、req000061。
//     旧实现要求数组最后一项就是 user，这一族全部漏修。
//
//  2. **那块 content[] 里必须全是 tool_result 块**，不是「没有 text 块」。
//     原判据（一个 text 块都没有就算）太宽，会把下面这些本来就能过的尾部也改掉：
//     [image] → 200、[tool_result, image] → 200。（tool_result + text → 200；
//     [tool_result] → 400；[tool_result, tool_result] 并行工具轮 → 400。）所以
//     「全是 tool_result」才是那条线：除 tool_result 之外的任何块（text、image）
//     都算「有指令」，上游就放行。
//
//  3. 调用点**不在 thinkingOn 闸门里**（见 Apply 里的注释）：thinking:disabled
//     且不带 tools 时这条校验照样触发。
//
// 已经带了文字/图片的尾部一律不碰——它本来就能过，多塞一句话只是往用户的对话里
// 加噪音。
//
// 用 AppendArrayItemArrayAt 而不是 AppendLastArrayItemArray：按下标挑。不写成
// 「从后往前找第一条合形状的」是因为长历史里中段的 tool_result-only 轮到处都是，
// 从后往前找会去改一条**不该动**的老消息。
func repairTailShape(body []byte) ([]byte, bool, error) {
	raw, ok := rewrite.TopLevelRaw(body, "messages")
	if !ok {
		return body, false, nil
	}
	items, ok := rewrite.ArrayItems(raw)
	if !ok || len(items) == 0 {
		return body, false, nil
	}

	// 1) 最后一条 role:user 消息的下标。
	idx := -1
	for i, it := range items {
		if role, _ := rewrite.TopLevelString(it, "role"); role == "user" {
			idx = i
		}
	}
	if idx < 0 {
		return body, false, nil
	}

	// 它之后**不能有 assistant 消息**。那种尾部（数组以 assistant 收尾）是另一条
	// 规则在管，而且追加指令证明**没有用**：实测 A1（tool_result-only 的 user 轮
	// + 尾随 assistant + 带 tools）3/3 400；把继续指令追加到那个 user 轮上就得到
	// A3，3/3 还是 400——换句话说那一族的 400 与「尾部有没有指令」无关（A3 的尾部
	// 本来就有文字）。既然改不好，就别改：往用户的对话里塞一句模型看不见效果的
	// 噪音，比 400 更糟。
	//
	// 那一族（带 tools 时数组以 assistant 收尾）从真实客户端到不了：Claude Code
	// 的请求要么以 user 的 tool_result 收尾（等模型接着干），要么以 user 的文字
	// 收尾，要么在两者之后追加一条 role:"system" 的插话。2026-09-17 把 dump 里
	// 全部 15 份 err-400 过了一遍，没有一份是 assistant 收尾。
	for _, it := range items[idx+1:] {
		if role, _ := rewrite.TopLevelString(it, "role"); role == "assistant" {
			return body, false, nil
		}
	}

	// 2) 它的 content[] 非空、且**全是** tool_result 块。
	content, ok := rewrite.TopLevelRaw(items[idx], "content")
	if !ok {
		return body, false, nil
	}
	blocks, ok := rewrite.ArrayItems(content)
	if !ok || len(blocks) == 0 {
		// content 是普通字符串（本来就有文字），或形状不认识：不动。
		return body, false, nil
	}
	for _, b := range blocks {
		if t, _ := rewrite.TopLevelString(b, "type"); t != "tool_result" {
			return body, false, nil
		}
	}

	q, err := json.Marshal(tailContinuation)
	if err != nil {
		return body, false, err
	}
	block := []byte(`{"type":"text","text":` + string(q) + `}`)
	out, changed, err := rewrite.AppendArrayItemArrayAt(body, "messages", "content", idx, block)
	if err != nil {
		return body, false, err
	}
	return out, changed, nil
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
// 这不是成功，用户有权在日志里一眼看出发生了多少次（docs/01-product.md 的「不静默」）。
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

// anthropicDialect 客户端这次发的是 Anthropic 方言吗（/v1/messages）。
//
// 判据用路径，不用 r.Protocol：protocol 说的是「怎么发到上游」（实测聚合
// 网关的 provider 全标 "openai"，却照样收 /v1/messages 的 Anthropic 方言
// 请求），而客户端发什么路径才是这次请求本身的方言。
func anthropicDialect(r *special.Request) bool {
	return r != nil && strings.HasPrefix(r.Path, "/messages")
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

// AuditReasoning 产出「本轮回传推理内容」的逐条审计报告，供 400 现场取证用。
//
// 直接解析 we-sent 的 messages，逐条 assistant 消息标出 reasoning_content 补的
// 是真实原文还是占位符。占位符正是这类「must be passed back」400 的直接诱因
// ——上游要逐字原文，占位符不是原文，必然被拒。所以取证必须能一眼看出是哪
// 几条补了占位符、它们的 tool_use id 是什么（方便反查缓存该不该有）。
//
// 这是纯只读分析，不依赖运行时的缓存状态——缓存此刻可能已经被后续请求顶掉，
// 但 400 发生时写下的这份报告是当时事实的定格。
func AuditReasoning(out []byte) string {
	msgs, ok := rewrite.TopLevelRaw(out, "messages")
	if !ok {
		return "（没有 messages 字段，无法审计）\n"
	}
	items, ok := rewrite.ArrayItems(msgs)
	if !ok {
		return "（messages 不是数组，无法审计）\n"
	}
	var b strings.Builder
	assistant, real, ph, missing := 0, 0, 0, 0
	for i, it := range items {
		if !isAssistant(it) {
			continue
		}
		assistant++
		rc, has := rewrite.TopLevelString(it, "reasoning_content")
		switch {
		case !has:
			missing++
			fmt.Fprintf(&b, "msg[%d] 缺失 reasoning_content  %s\n", i, msgKeys(it))
		case rc == reasoningFallback:
			ph++
			fmt.Fprintf(&b, "msg[%d] 占位符（非原文，上游会拒）  %s\n", i, msgKeys(it))
		default:
			real++
		}
	}
	return fmt.Sprintf("assistant 共 %d 条：真实原文 %d，占位符 %d，缺失 %d\n"+
		"占位符/缺失就是上游「must be passed back」的直接诱因，逐条：\n%s",
		assistant, real, ph, missing, b.String())
}

func (reasoning) AuditResponse(body []byte) string { return AuditReasoning(body) }

// msgKeys 抽出这条 assistant 消息的 tool_use id（缓存找回的 key 就靠它），
// 纯文本轮没有 tool_use，靠正文哈希。取证时拿 id 反查 thinkcache 该不该有。
func msgKeys(item []byte) string {
	var toolIDs []string
	if c, ok := rewrite.TopLevelRaw(item, "content"); ok {
		if bs, ok := rewrite.ArrayItems(c); ok {
			for _, blk := range bs {
				if t, _ := rewrite.TopLevelString(blk, "type"); t != "tool_use" {
					continue
				}
				if id, _ := rewrite.TopLevelString(blk, "id"); id != "" {
					toolIDs = append(toolIDs, id)
				}
			}
		}
	}
	if len(toolIDs) == 0 {
		return "（纯文本轮，无 tool_use）"
	}
	return "tool_use ids: " + strings.Join(toolIDs, " ")
}
