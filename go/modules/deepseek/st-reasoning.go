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