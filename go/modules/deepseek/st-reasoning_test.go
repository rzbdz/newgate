package deepseek

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
	"github.com/rzbdz/newgate/go/modules/gateway/thinkcache"
)

// claudeReq Claude Code 形态的上下文：/a/claude/ + /v1/messages。
//
// 这个包里的 deepseek 测试默认都是「Claude Code 打 DeepSeek 网关」这一幕
// ——插件就是为它写的。opencode 那条路（Agent 空 + /chat/completions）在
// TestDeepSeekOpencodeKeepsThinking 里单独立着。
func claudeReq(model string) *special.Request {
	return &special.Request{Model: model, Provider: "gw", BaseURL: "https://gw.example.com/v1",
		Protocol: "anthropic", Path: "/messages", Agent: "claude"}
}

// 思考模式开着时，assistant 的 content[] 必须带 thinking 块，否则 DeepSeek
// 回 400 The `content[].thinking` in the thinking mode must be passed back。
// 客户端（Claude Code）对非官方端点会主动剥掉这些块，所以只能我们补。
func TestDeepSeekBackfillsThinkingBlockWhenThinkingOn(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"n":9007199254740993}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("改了东西却没回报 notes —— 违反「不静默」")
	}

	var got struct {
		Thinking json.RawMessage `json:"thinking"`
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}

	// 客户端显式要了思考模式，不能被我们悄悄关掉
	if !strings.Contains(string(got.Thinking), "enabled") {
		t.Fatalf("客户端的 thinking 被改掉了: %s", got.Thinking)
	}

	// assistant 的 thinking 块必须在**开头**：协议要求它排在 tool_use 之前
	asst := got.Messages[1]
	if len(asst.Content) != 2 {
		t.Fatalf("assistant content 块数 = %d，想要 2", len(asst.Content))
	}
	if !strings.Contains(string(asst.Content[0]), `"type":"thinking"`) {
		t.Fatalf("第一个块不是 thinking: %s", asst.Content[0])
	}
	if !strings.Contains(string(asst.Content[1]), "tool_use") {
		t.Fatalf("原来的 tool_use 块丢了: %s", asst.Content[1])
	}

	// user 消息一个字节都不该被碰
	if strings.Contains(string(got.Messages[0].Content[0]), "thinking") {
		t.Fatal("user 消息被补了 thinking 块")
	}

	// 大整数必须原样：JSON 往返会把它变成 ...92
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("大整数被改坏了:\n%s", out)
	}
}

// 思考模式没开时塞 thinking 块，会被上游以「关了还给我思考块」拒掉。
func TestDeepSeekNoThinkingBlockWhenDisabled(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"deepseek-chat","thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`),
	} {
		out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Contains(string(out), `"type":"thinking"`) {
			t.Fatalf("思考关着还是补了 thinking 块:\n%s", out)
		}
	}
}

// 已经带思考内容的消息一个字节都不动——包括上游加密过的 redacted_thinking。
func TestDeepSeekLeavesExistingThinkingAlone(t *testing.T) {
	for _, blk := range []string{
		`{"type":"thinking","thinking":"我在想","signature":"abc"}`,
		`{"type":"redacted_thinking","data":"xxx"}`,
	} {
		body := []byte(`{"thinking":{"type":"enabled"},"messages":[` +
			`{"role":"assistant","content":[` + blk + `,{"type":"text","text":"hi"}]}]}`)
		out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Count(string(out), `"type":"thinking"`)+
			strings.Count(string(out), `"type":"redacted_thinking"`) != 1 {
			t.Fatalf("给已有思考内容的消息又补了一块:\n%s", out)
		}
	}
}

// content 是纯字符串（Anthropic 允许）时没有块可插，跳过而不是报错。
func TestDeepSeekStringContentSkipped(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":"hi"}]}`)
	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("往字符串 content 里插了块:\n%s", out)
	}
	if !json.Valid(out) {
		t.Fatalf("改完不是合法 JSON:\n%s", out)
	}
}

// reasoning_effort 与 thinking:disabled 互斥，上游会回
// 「thinking options type cannot be disabled when reasoning_effort is set」。
// 设了推理强度就别去关思考，改成补块。
func TestDeepSeekRespectsReasoningEffort(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","reasoning_effort":"high",` +
		`"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`)
	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"disabled"`) {
		t.Fatalf("设了 reasoning_effort 还去关思考:\n%s", out)
	}
	if !strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("没补 thinking 块:\n%s", out)
	}
	if strings.Contains(string(out), `"thinking":""`) {
		t.Fatalf("补了空 thinking 块——严格上游照样 400:\n%s", out)
	}
}

// 缓存查不到、客户端也没带回思考块时，绝不能补空串：2026-09 在 new-api
// 直连 DeepSeek 官方接口的部署上实抓到，最严的检查连空串都以
// 「The reasoning_content in the thinking mode must be passed back」拒掉。
// 占位符必须非空。
func TestDeepSeekPlaceholderNeverEmpty(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t-never-cached-0001","name":"Read","input":{"n":9007199254740993}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-never-cached-0001","content":"ok"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
			Content          []struct {
				Type     string `json:"type"`
				Thinking string `json:"thinking"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}

	a := got.Messages[0]
	if a.ReasoningContent == "" {
		t.Fatalf("reasoning_content 为空——严格上游会 400:\n%s", out)
	}
	if len(a.Content) != 2 || a.Content[0].Type != "thinking" || a.Content[0].Thinking == "" {
		t.Fatalf("thinking 块缺失或为空:\n%s", out)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "占位符") {
		t.Fatalf("占位符回填没在 notes 里说清楚（违反「不静默」）: %v", notes)
	}
}

// 客户端自己带回的 thinking 块是推理原文：reasoning_content 直接用它的文本
// （不依赖缓存存活），也不再重复补块。
func TestDeepSeekUsesClientThinkingBlock(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"我先想想","signature":"sig"},` +
		`{"type":"text","text":"答案"}]}]}`)

	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
			Content          []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}

	a := got.Messages[0]
	if a.ReasoningContent != "我先想想" {
		t.Fatalf("reasoning_content 应取客户端 thinking 块原文，实际 %q", a.ReasoningContent)
	}
	if len(a.Content) != 2 {
		t.Fatalf("给已有 thinking 块的消息又补了一块:\n%s", out)
	}
}

// thinkcache 命中：reasoning_content 和 thinking 块都回填那轮真实的推理原文。
func TestDeepSeekUsesCacheReasoning(t *testing.T) {
	const toolID = "t-cache-hit-0002"
	thinkcache.Default.Put([]string{thinkcache.ToolKey(toolID)}, []byte("这轮真实的推理"))

	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + toolID + `","name":"Bash","input":{}}]}` +
		`]}`)

	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got struct {
		Messages []struct {
			ReasoningContent string `json:"reasoning_content"`
			Content          []struct {
				Type     string `json:"type"`
				Thinking string `json:"thinking"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}

	a := got.Messages[0]
	if a.ReasoningContent != "这轮真实的推理" {
		t.Fatalf("reasoning_content 应取缓存原文，实际 %q", a.ReasoningContent)
	}
	if len(a.Content) == 0 || a.Content[0].Thinking != "这轮真实的推理" {
		t.Fatalf("thinking 块应取缓存原文:\n%s", out)
	}
}

// 计数诚实：第 3 步（thinking 块）的占位符不能被当成「真实原文」。
//
// 修复前的 bug：第 2 步给没缓存的消息补了 reasoning_content（占位符），
// 第 3 步读回这个字段，把占位符算成「真实原文」——日志里 thinking 块的
// 计数虚高。修复后第 3 步和第 2 步用同一份来源（pickReasoning），占位符
// 如实报成占位符。
func TestDeepSeekThinkingBlockPlaceholderCountedHonestly(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"t-never-cached-0003","name":"Read","input":{}}]}]}`)

	_, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	all := strings.Join(notes, "\n")
	// 两步各报一次「只能补占位符」：reasoning_content 一步、thinking 块一步
	if n := strings.Count(all, "只能补占位符"); n != 2 {
		t.Fatalf("占位符该报 2 次（reasoning_content + thinking 块），实际 %d 次:\n%s", n, all)
	}
	// thinking 块那步绝不能把占位符说成「用了真实的推理原文」
	if strings.Contains(all, "补 thinking 块：1 条用了真实的推理原文") {
		t.Fatalf("thinking 块的占位符被虚报成真实原文:\n%s", all)
	}
}

// ---- opencode（裸 /v1 + OpenAI 方言）这条路上不能关思考 ----

// 现场（2026-09-15）：opencode 走 OpenAI 方言，压根不会写 thinking 这个
// Anthropic 字段，于是条条请求都被当成「客户端没要思考」补上 disabled——
// 用户看到的是「deepseek somehow 不思考了」。
//
// 修后：关思考只给 Claude Code（Agent=="claude"）。这里 Agent 空、路径
// /chat/completions，就是 opencode 的形态：thinking 一个字节都不许动，
// 但推理内容照旧替它回传（思考开着，上游就会要）。
func TestDeepSeekOpencodeKeepsThinking(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash","stream":true,"messages":[` +
		`{"role":"user","content":"看下这个文件"},` +
		`{"role":"assistant","content":"看完了","tool_calls":[` +
		`{"id":"call_oa_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}` +
		`]}`)

	r := &special.Request{Model: "deepseek-flash", Provider: "smt-deepseek",
		BaseURL: "https://gw.example.com/v1", Protocol: "openai",
		Path: "/chat/completions"}
	out, notes, err := reasoning{}.Apply(body, r)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"thinking"`) {
		t.Fatalf("opencode 的请求被关了思考（现场 bug）:\n%s", out)
	}

	var got struct {
		Messages []struct {
			ReasoningContent string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}
	if got.Messages[1].ReasoningContent == "" {
		t.Fatalf("思考开着就必须回传推理内容，缺了上游要 400:\n%s", out)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "reasoning_content") {
		t.Fatalf("改了 reasoning_content 却没回报（违反「不静默」）: %v", notes)
	}
}

// 第 3 手（content[] 开头的 thinking 块）是 Anthropic 方言的协议要求；
// OpenAI 方言里回传推理的载体是 reasoning_content，往人家的 content[] 里
// 塞一个上游不认识的块类型只会招 400。
func TestDeepSeekNoThinkingBlockInOpenAIDialect(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"t-oa-0001","name":"Read","input":{}}]}]}`)

	out, _, err := reasoning{}.Apply(body, &special.Request{Model: "deepseek-flash",
		Provider: "smt-deepseek", BaseURL: "https://gw.example.com/v1",
		Path: "/chat/completions"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("往 OpenAI 方言的 content[] 里塞了 thinking 块:\n%s", out)
	}
	if !strings.Contains(string(out), `"reasoning_content"`) {
		t.Fatalf("reasoning_content 该照补:\n%s", out)
	}
}

func TestDeepSeekToolLoopMigrationIsScopedAndRebased(t *testing.T) {
	ds := reasoning{}
	candidate := &special.Request{
		Model: "deepseek-flash", Provider: "smt-deepseek",
		BaseURL: "https://gw.example.com/v1", Path: "/messages",
	}
	if needed, _ := ds.NeedsToolLoopRebase("ark", "ark-code-latest", candidate); !needed {
		t.Fatal("DeepSeek 接手 Ark tool loop 应要求 rebase")
	}
	if needed, _ := ds.NeedsToolLoopRebase(
		"smt-deepseek", "deepseek-flash", candidate); needed {
		t.Fatal("DeepSeek 续自己的 tool loop 不该 rebase")
	}
	ark := &special.Request{Model: "ark-code-latest", Provider: "ark",
		BaseURL: "https://ark.example.com", Path: "/messages"}
	if needed, _ := ds.NeedsToolLoopRebase("smt-deepseek", "deepseek-flash", ark); needed {
		t.Fatal("deepseek special 不该约束迁出到 Ark")
	}

	body := []byte(`{"model":"deepseek-flash","messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)
	out, note, err := ds.RebaseToolLoop(body, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), toolLoopRebasePrompt) {
		t.Fatalf("没有追加 rebase 指令:\n%s", out)
	}
	if !strings.Contains(note, "有损重建") {
		t.Fatalf("rebase 没有明确回报: %q", note)
	}
}

// TestTailShapeRepairOnToolResultOnlyTail 锁住 reasoning 400 的**根因**修复。
//
// 现场（dump/err-400-req000412、req000464，2026-09-17）：229 条消息、每条
// assistant 都带着 thinking 块和 reasoning_content、tools 开着、thinking
// adaptive，上游照样回「reasoning_content must be passed back」。排除法得出
// 真正起作用的是尾部形状：最后一条 user 消息只有 tool_result、没有任何文字。
func TestTailShapeRepairOnToolResultOnlyTail(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash","thinking":{"type":"adaptive"},"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"想一下"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-flash"))
	if err != nil {
		t.Fatalf("Apply 报错: %v", err)
	}
	if !strings.Contains(string(out), "Continue from the tool results above") {
		t.Fatalf("尾部没有补上继续指令:\n%s", out)
	}
	// 原内容一个字节都不能丢：tool_result 还在，历史消息没被动。
	if !strings.Contains(string(out), `"tool_use_id":"t1"`) ||
		!strings.Contains(string(out), `"text":"开始吧"`) {
		t.Fatalf("补尾部指令时改动了已有内容:\n%s", out)
	}
	// 继续指令必须在**最后一条** user 消息里（尾部），不是别的地方。
	tail := string(out[strings.LastIndex(string(out), `"role":"user"`):])
	if !strings.Contains(tail, "Continue from the tool results above") {
		t.Fatalf("继续指令没落在尾部消息里:\n%s", tail)
	}
	if !containsNote(notes, "尾") {
		t.Fatalf("没有回报 notes（不静默是硬要求）: %v", notes)
	}
}

// TestTailShapeLeavesNormalTailsAlone：尾部本来就有文字 / 尾部不是 user /
// 思考关着 —— 三种情况都不许动，别往用户对话里加噪音。
func TestTailShapeLeavesNormalTailsAlone(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"尾部有文字", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"尾部是 assistant", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
			`{"role":"assistant","content":[{"type":"text","text":"说完了"}]}]}`},
		{"content 是字符串", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":"纯文本"}]}`},
		{"没有 messages", `{"thinking":{"type":"adaptive"}}`},
		{"messages 为空", `{"thinking":{"type":"adaptive"},"messages":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := reasoning{}.Apply([]byte(tt.body),
				claudeReq("deepseek-flash"))
			if err != nil {
				t.Fatalf("Apply 报错: %v", err)
			}
			if strings.Contains(string(out), "Continue from the tool results above") {
				t.Fatalf("不该动的尾部被动了:\n%s", out)
			}
		})
	}
}

// TestTailShapeSkippedWhenThinkingOff：思考关着时 DeepSeek 不走那条严格校验，
// 补了反而是噪音。
func TestTailShapeSkippedWhenThinkingOff(t *testing.T) {
	body := []byte(`{"thinking":{"type":"disabled"},"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`)
	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-flash"))
	if err != nil {
		t.Fatalf("Apply 报错: %v", err)
	}
	if strings.Contains(string(out), "Continue from the tool results above") {
		t.Fatalf("思考关着却补了尾部指令:\n%s", out)
	}
}

func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
