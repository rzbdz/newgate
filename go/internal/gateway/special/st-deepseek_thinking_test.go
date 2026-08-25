package special

import (
	"encoding/json"
	"strings"
	"testing"
)

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

	out, notes, err := deepseek{}.Apply(body, &Request{Model: "deepseek-chat"})
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
		[]byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`),
		[]byte(`{"model":"deepseek-chat","thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`),
	} {
		out, _, err := deepseek{}.Apply(body, &Request{Model: "deepseek-chat"})
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
		out, _, err := deepseek{}.Apply(body, &Request{Model: "deepseek-chat"})
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
	out, _, err := deepseek{}.Apply(body, &Request{Model: "deepseek-chat"})
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
	out, _, err := deepseek{}.Apply(body, &Request{Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"disabled"`) {
		t.Fatalf("设了 reasoning_effort 还去关思考:\n%s", out)
	}
	if !strings.Contains(string(out), `{"type":"thinking","thinking":""}`) {
		t.Fatalf("没补 thinking 块:\n%s", out)
	}
}
