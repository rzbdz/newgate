package upstream_test

// 这个文件自测假上游本身：两种方言的非流式与流式都能通、count_tokens 不是 42
// 以外的值、严格口径真的会 400（先证明会拦，再证明补齐推理之后放行）、
// FailNext 只影响一发、Requests/Reset 与记录面的形状都没坏。
//
// 为什么值得专门测一个测试替身：e2e 的全部结论都建立在「假上游确实按官方口径
// 拦过」之上——假上游要是自己漏判，后面那些 200 一文不值（mock/e2e_claude.sh
// 第 6 章专门先自检这一点，这里对应 TestStrictReasoning）。

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate/go/testing/upstream"
)

// ---- 小工具 ----

// send 发一发请求，返回原始响应（调用方负责关 Body；要看响应头的也走它）。
func send(t *testing.T, s *upstream.Server, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.URL()+path, reader)
	if err != nil {
		t.Fatalf("建请求 %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// post 发一发 POST 并读回状态码与响应体原文。
func post(t *testing.T, s *upstream.Server, path, body string) (int, string) {
	t.Helper()
	resp := send(t, s, http.MethodPost, path, body, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读 %s 的响应体: %v", path, err)
	}
	return resp.StatusCode, string(raw)
}

// get 发一发 GET。
func get(t *testing.T, s *upstream.Server, path string) (int, string) {
	t.Helper()
	resp := send(t, s, http.MethodGet, path, "", nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读 %s 的响应体: %v", path, err)
	}
	return resp.StatusCode, string(raw)
}

// decodeObj 解一个 JSON 对象，解不动就 Fatal。
func decodeObj(t *testing.T, raw string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("不是 JSON 对象: %v（原文：%s）", err, raw)
	}
	return obj
}

// dig 沿路径取值（字符串 = 对象键，整数 = 数组下标），取不到就 Fatal。
// 断言里到处要 dig(..., "choices", 0, "message", "content")，摊平了才看得清。
func dig(t *testing.T, value any, path ...any) any {
	t.Helper()
	current := value
	for _, step := range path {
		switch key := step.(type) {
		case string:
			obj, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("取 %v：%v 不是对象", path, current)
			}
			field, ok := obj[key]
			if !ok {
				t.Fatalf("取 %v：没有 %q（实际键：%v）", path, key, obj)
			}
			current = field
		case int:
			arr, ok := current.([]any)
			if !ok {
				t.Fatalf("取 %v：%v 不是数组", path, current)
			}
			if key >= len(arr) {
				t.Fatalf("取 %v：下标 %d 越界（长度 %d）", path, key, len(arr))
			}
			current = arr[key]
		default:
			t.Fatalf("取 %v：路径段只能是 string 或 int", path)
		}
	}
	return current
}

// sseEvents 把 SSE 响应体切成事件：每块一行 "data: <json>"、块间空行，
// 收尾的 [DONE] 不是 JSON，单独用一个布尔返回。
func sseEvents(t *testing.T, body string) (events []map[string]any, done bool) {
	t.Helper()
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") {
			t.Fatalf("SSE 块不是 data:: %q", block)
		}
		payload := strings.TrimPrefix(block, "data: ")
		if payload == "[DONE]" {
			done = true
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("SSE 块不是 JSON: %v（原文：%s）", err, payload)
		}
		events = append(events, event)
	}
	return events, done
}

// eventTypes 抽出事件类型序列，用来断言 Anthropic 的整条块序列。
func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		kind, _ := event["type"].(string)
		out = append(out, kind)
	}
	return out
}

// ---- openai 方言 ----

func TestOpenAINonStream(t *testing.T) {
	s := upstream.New(t)
	payload := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`

	resp := send(t, s, http.MethodPost, "/v1/chat/completions", payload, map[string]string{
		"Authorization":     "Bearer sk-test",
		"Anthropic-Version": "2023-06-01",
	})
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（体：%s）", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", ct)
	}

	body := decodeObj(t, string(raw))
	if got := dig(t, body, "id"); got != upstream.ChatCompletionID {
		t.Errorf("id = %v，期望 %s", got, upstream.ChatCompletionID)
	}
	if got := dig(t, body, "object"); got != "chat.completion" {
		t.Errorf("object = %v，期望 chat.completion", got)
	}
	if got := dig(t, body, "model"); got != "deepseek-chat" {
		t.Errorf("model = %v（响应要把请求里的 model 原样回显）", got)
	}
	if got := dig(t, body, "choices", 0, "message", "content"); got != "MOCK-OK model=deepseek-chat" {
		t.Errorf("choices[0].message.content = %v", got)
	}
	if got := dig(t, body, "choices", 0, "finish_reason"); got != "stop" {
		t.Errorf("finish_reason = %v", got)
	}
	if got := dig(t, body, "usage", "total_tokens"); got != float64(12) {
		t.Errorf("usage.total_tokens = %v，期望 12", got)
	}

	// 记录面：代理到底发出去了什么，全靠它
	if s.Count() != 1 {
		t.Fatalf("记录数 = %d，期望 1", s.Count())
	}
	rec, ok := s.Last()
	if !ok {
		t.Fatal("Last() 说没有记录")
	}
	if rec.Method != http.MethodPost {
		t.Errorf("记录的方法 = %q", rec.Method)
	}
	if rec.Path != "/v1/chat/completions" {
		t.Errorf("记录的路径 = %q", rec.Path)
	}
	if got := rec.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("记录的 Authorization = %q", got)
	}
	if got := rec.Header.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Errorf("记录的 Anthropic-Version = %q（http.Header 的键不区分大小写）", got)
	}
	if string(rec.Body) != payload {
		t.Errorf("记录的请求体 = %q，期望原样字节 %q", rec.Body, payload)
	}
	if rec.At.IsZero() || rec.At.After(time.Now()) {
		t.Errorf("记录的时间 = %v，不对", rec.At)
	}
	fields, err := rec.JSON()
	if err != nil {
		t.Fatalf("Record.JSON(): %v", err)
	}
	if got := fields["model"]; got != "deepseek-chat" {
		t.Errorf("JSON() 里的 model = %v", got)
	}
}

func TestOpenAIStream(t *testing.T) {
	s := upstream.New(t)
	s.SetChunkDelay(0) // 协议用例不必真等

	resp := send(t, s, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-4-plus","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q，期望 text/event-stream", got)
	}
	// 分帧要跟 python 版一样：没有长度、也不分块（体以连接关闭为界）。
	// Go 默认会给无长度的 HTTP/1.1 响应加 chunked，那与 bash e2e 里网关
	// 看到的分帧就不是一回事了。
	if len(resp.TransferEncoding) != 0 {
		t.Errorf("响应被分块了（%v）——Transfer-Encoding: identity 那行被删了？", resp.TransferEncoding)
	}
	if resp.ContentLength != -1 {
		t.Errorf("ContentLength = %d，流式响应不该有长度", resp.ContentLength)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	events, done := sseEvents(t, string(raw))
	if !done {
		t.Error("流没有以 data: [DONE] 收尾")
	}
	if len(events) != 5 {
		t.Fatalf("事件数 = %d，期望 4 个增量块 + 1 个收尾块（实际类型：%v）", len(events), eventTypes(events))
	}

	var text strings.Builder
	for _, event := range events[:4] {
		if got := dig(t, event, "object"); got != "chat.completion.chunk" {
			t.Errorf("块的 object = %v，期望 chat.completion.chunk", got)
		}
		if got := dig(t, event, "model"); got != "glm-4-plus" {
			t.Errorf("块的 model = %v", got)
		}
		if got := dig(t, event, "choices", 0, "index"); got != float64(0) {
			t.Errorf("块的 index = %v", got)
		}
		text.WriteString(dig(t, event, "choices", 0, "delta", "content").(string))
	}
	if got := text.String(); got != "MOCK-STREAM glm-4-plus" {
		t.Errorf("增量块拼起来 = %q，期望 MOCK-STREAM glm-4-plus", got)
	}
	if got := dig(t, events[4], "choices", 0, "finish_reason"); got != "stop" {
		t.Errorf("收尾块的 finish_reason = %v", got)
	}
}

// ---- anthropic 方言 ----

func TestAnthropicNonStream(t *testing.T) {
	s := upstream.New(t)
	withTools := `{"model":"glm-4-plus","max_tokens":64,` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"go"}]}`

	for _, tc := range []struct {
		name string
		path string
		body string
		want int // 期望 content 里几块
	}{
		{"带 tools：thinking + tool_use + text", "/v1/messages", withTools, 3},
		{"带前缀的路径也认（网关剥前缀前/直连假上游时）", "/a/claude/p/ds/v1/messages", withTools, 3},
		{"不带 tools：没有 tool_use 块", "/v1/messages",
			`{"model":"glm-4-plus","max_tokens":64,"messages":[{"role":"user","content":"go"}]}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := post(t, s, tc.path, tc.body)
			if code != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200（体：%s）", code, raw)
			}
			body := decodeObj(t, raw)
			if got := dig(t, body, "id"); got != upstream.MessageID {
				t.Errorf("id = %v", got)
			}
			if got := dig(t, body, "type"); got != "message" {
				t.Errorf("type = %v", got)
			}
			if got := dig(t, body, "model"); got != "glm-4-plus" {
				t.Errorf("model = %v", got)
			}
			if got := dig(t, body, "stop_reason"); got != "end_turn" {
				t.Errorf("stop_reason = %v", got)
			}
			if got := dig(t, body, "usage", "input_tokens"); got != float64(7) {
				t.Errorf("usage.input_tokens = %v", got)
			}

			content, ok := dig(t, body, "content").([]any)
			if !ok {
				t.Fatalf("content 不是数组: %v", dig(t, body, "content"))
			}
			if len(content) != tc.want {
				t.Fatalf("content 块数 = %d，期望 %d", len(content), tc.want)
			}
			// 顺序是语义：thinking 在前、紧跟 tool_use——thinkcache 靠这个相邻
			// 关系把推理原文和 tool id 绑起来。
			if got := dig(t, content, 0, "type"); got != "thinking" {
				t.Errorf("content[0].type = %v，期望 thinking", got)
			}
			if got := dig(t, content, 0, "thinking"); got != upstream.ThinkingText {
				t.Errorf("content[0].thinking = %v", got)
			}
			if got := dig(t, content, 0, "signature"); got != upstream.ThinkingSignature {
				t.Errorf("content[0].signature = %v", got)
			}
			if tc.want == 3 {
				if got := dig(t, content, 1, "type"); got != "tool_use" {
					t.Errorf("content[1].type = %v，期望 tool_use", got)
				}
				if got := dig(t, content, 1, "id"); got != upstream.ToolUseID {
					t.Errorf("content[1].id = %v，期望 %s", got, upstream.ToolUseID)
				}
				if got := dig(t, content, 1, "name"); got != upstream.ToolName {
					t.Errorf("content[1].name = %v", got)
				}
			}
			last := len(content) - 1
			if got := dig(t, content, last, "type"); got != "text" {
				t.Errorf("最后一块的 type = %v，期望 text", got)
			}
			if got := dig(t, content, last, "text"); got != "MOCK-OK model=glm-4-plus" {
				t.Errorf("最后一块的 text = %v", got)
			}
		})
	}
}

func TestAnthropicStream(t *testing.T) {
	s := upstream.New(t)
	s.SetChunkDelay(0)
	withTools := `{"model":"glm-4-plus","stream":true,"max_tokens":64,` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"go"}]}`

	resp := send(t, s, http.MethodPost, "/v1/messages", withTools, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	events, done := sseEvents(t, string(raw))
	if !done {
		t.Error("流没有以 data: [DONE] 收尾（python 版也发这一行）")
	}

	// 整条块序列：Claude Code 的真实形态（thinking 块在前、tools 请求加
	// tool_use 块、正文块在后）。
	want := []string{
		"message_start",
		"content_block_start", // index 0：thinking
		"content_block_delta", // 推理原文
		"content_block_start", // index 1：tool_use
		"content_block_start", // index 2：text
		"content_block_delta", "content_block_delta", "content_block_delta", "content_block_delta",
		"message_stop",
	}
	got := eventTypes(events)
	if len(got) != len(want) {
		t.Fatalf("块序列 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 块的 type = %q，期望 %q（整条：%v）", i, got[i], want[i], got)
		}
	}

	if got := dig(t, events[0], "message", "id"); got != upstream.MessageID {
		t.Errorf("message_start 的 message.id = %v", got)
	}
	if got := dig(t, events[0], "message", "model"); got != "glm-4-plus" {
		t.Errorf("message_start 的 message.model = %v", got)
	}
	if got := dig(t, events[0], "message", "content"); got == nil {
		t.Error("message_start 的 message.content 该是空数组")
	}
	if got := dig(t, events[1], "index"); got != float64(0) {
		t.Errorf("thinking 块的 index = %v", got)
	}
	if got := dig(t, events[1], "content_block", "type"); got != "thinking" {
		t.Errorf("第 1 块的 content_block.type = %v", got)
	}
	if got := dig(t, events[2], "delta", "type"); got != "thinking_delta" {
		t.Errorf("第 2 块的 delta.type = %v", got)
	}
	if got := dig(t, events[2], "delta", "thinking"); got != upstream.ThinkingText {
		t.Errorf("thinking_delta 的内容 = %v", got)
	}
	if got := dig(t, events[3], "content_block", "id"); got != upstream.ToolUseID {
		t.Errorf("tool_use 块的 id = %v，期望 %s", got, upstream.ToolUseID)
	}
	if got := dig(t, events[3], "index"); got != float64(1) {
		t.Errorf("tool_use 块的 index = %v", got)
	}
	if got := dig(t, events[4], "content_block", "type"); got != "text" {
		t.Errorf("正文块的 type = %v", got)
	}
	if got := dig(t, events[4], "index"); got != float64(2) {
		t.Errorf("正文块的 index = %v", got)
	}
	var text strings.Builder
	for _, event := range events[5:9] {
		if got := dig(t, event, "index"); got != float64(2) {
			t.Errorf("正文增量的 index = %v，该指向正文块", got)
		}
		if got := dig(t, event, "delta", "type"); got != "text_delta" {
			t.Errorf("正文增量的 delta.type = %v", got)
		}
		text.WriteString(dig(t, event, "delta", "text").(string))
	}
	if got := text.String(); got != "MOCK-STREAM glm-4-plus" {
		t.Errorf("正文增量拼起来 = %q", got)
	}

	// 不带 tools 时不该有 tool_use 块，正文块的下标跟着回到 1
	resp2 := send(t, s, http.MethodPost, "/v1/messages",
		`{"model":"glm-4-plus","stream":true,"messages":[{"role":"user","content":"go"}]}`, nil)
	defer resp2.Body.Close()
	raw2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	events2, _ := sseEvents(t, string(raw2))
	if got := eventTypes(events2)[:4]; len(got) != 4 || got[3] != "content_block_start" {
		t.Fatalf("块序列 = %v", got)
	}
	if got := dig(t, events2[3], "content_block", "type"); got != "text" {
		t.Errorf("没有 tools 时第 3 块该是 text，实际 %v", got)
	}
	if got := dig(t, events2[3], "index"); got != float64(1) {
		t.Errorf("没有 tools 时正文块 index = %v，期望 1", got)
	}
}

// ---- count_tokens ----

func TestCountTokens(t *testing.T) {
	s := upstream.New(t)
	tools := `"tools":[{"name":"Read","input_schema":{"type":"object"}}]`

	for _, tc := range []struct{ name, path, body string }{
		{"Claude Code 的形态（不带 model）", "/v1/messages/count_tokens",
			`{"messages":[{"role":"user","content":"数一下 token"}],` + tools + `}`},
		{"带前缀的变体", "/a/claude/p/ds/v1/messages/count_tokens",
			`{"messages":[{"role":"user","content":"数一下 token"}]}`},
		{"历史里 assistant 没带推理也要放行（判严格口径在 count_tokens 之后）",
			`/v1/messages/count_tokens`,
			`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]}],` + tools + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := post(t, s, tc.path, tc.body)
			if code != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200（体：%s）", code, raw)
			}
			body := decodeObj(t, raw)
			if got := dig(t, body, "input_tokens"); got != float64(42) {
				t.Errorf("input_tokens = %v，期望 42", got)
			}
		})
	}
}

// ---- 严格口径 ----

// TestStrictReasoning 是 e2e 最重要那条判据的自检：**先证明假上游真的会拦**
// （前几条必须 400），再证明补齐推理之后放行（后几条必须 200）。顺序有意如此
// ——反过来的话，一个从不校验的假上游也能让所有用例「通过」。
func TestStrictReasoning(t *testing.T) {
	const tools = `"tools":[{"name":"Read","input_schema":{"type":"object"}}]`
	const thinkingOn = `"thinking":{"type":"enabled","budget_tokens":1024}`
	const toolUse = `{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}`

	for _, tc := range []struct {
		name string
		path string
		body string
		want int
	}{
		{
			// Claude Code 把 thinking 块剥掉之后的现场：两条判据都是空的
			name: "带 tools + 思考开 + assistant 只剩 tool_use",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","content":[` + toolUse + `]}]}`,
			want: http.StatusBadRequest,
		},
		{
			// 空串等于没回传——e2e 第 6 章专抓这一条（补占位符时补成空串
			// 的实现会在这里露馅）
			name: "reasoning_content 是空串",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","reasoning_content":"","content":[` + toolUse + `]}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "reasoning_content 只有空白",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","reasoning_content":"  \n\t","content":[` + toolUse + `]}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "content[] 里的 thinking 块是空文本",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":""},` + toolUse + `]}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "assistant 的 content 是普通字符串、没有推理",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","content":"好的"}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "没带 thinking 字段（国模缺省就是思考）也算开着",
			body: `{"model":"deepseek-chat",` + tools +
				`,"messages":[{"role":"assistant","content":[` + toolUse + `]}]}`,
			want: http.StatusBadRequest,
		},
		{
			// openai 方言（聚合器 /v1/chat/completions）同一个口径
			name: "openai 方言同样严格",
			path: "/v1/chat/completions",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","content":"好的"}]}`,
			want: http.StatusBadRequest,
		},

		{
			// 放行路一：openai 方言的 reasoning_content 字段
			name: "补齐非空 reasoning_content",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","reasoning_content":"缓存里的原文","content":[` + toolUse + `]}]}`,
			want: http.StatusOK,
		},
		{
			// 放行路二：anthropic 方言 content[] 里的 thinking 块
			name: "补齐非空 thinking 块",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"缓存里的原文"},` + toolUse + `]}]}`,
			want: http.StatusOK,
		},
		{
			name: "thinking: disabled 就不校验了",
			body: `{"model":"deepseek-chat","thinking":{"type":"disabled"},` + tools +
				`,"messages":[{"role":"assistant","content":[` + toolUse + `]}]}`,
			want: http.StatusOK,
		},
		{
			name: "没带 tools 时不校验",
			body: `{"model":"deepseek-chat",` + thinkingOn +
				`,"messages":[{"role":"assistant","content":[` + toolUse + `]}]}`,
			want: http.StatusOK,
		},
		{
			name: "tools 是空数组（python 里空列表 falsy）",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,"tools":[],` +
				`"messages":[{"role":"assistant","content":[` + toolUse + `]}]}`,
			want: http.StatusOK,
		},
		{
			name: "只管 assistant：user 消息没有推理不算",
			body: `{"model":"deepseek-chat",` + thinkingOn + `,` + tools +
				`,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"}]}]}`,
			want: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := upstream.New(t)
			path := tc.path
			if path == "" {
				path = "/v1/messages"
			}
			code, raw := post(t, s, path, tc.body)
			if code != tc.want {
				t.Fatalf("状态码 = %d，期望 %d（体：%s）", code, tc.want, raw)
			}
			if s.Count() != 1 {
				t.Fatalf("这一发该被记录（无论拦没拦），记录数 = %d", s.Count())
			}
			if tc.want == http.StatusOK {
				// 放行：必须是那条正常响应，不是别的什么错误
				if !strings.Contains(raw, "MOCK-OK model=deepseek-chat") {
					t.Errorf("放行了但响应体不是假上游的正常回复：%s", raw)
				}
				return
			}
			// 拦下了：错误文本必须逐字是 python 版那句——判据是它，不是状态码
			if !strings.Contains(raw, upstream.StrictReasoningMessage) {
				t.Errorf("400 的错误文本里没有那句话: %s", raw)
			}
			body := decodeObj(t, raw)
			if got := dig(t, body, "type"); got != "error" {
				t.Errorf("type = %v，期望 error", got)
			}
			if got := dig(t, body, "error", "type"); got != "invalid_request_error" {
				t.Errorf("error.type = %v", got)
			}
			if got := dig(t, body, "error", "message"); got != upstream.StrictReasoningMessage {
				t.Errorf("error.message = %v", got)
			}
		})
	}
}

// ---- 控制面 ----

func TestFailNextIsOneShot(t *testing.T) {
	s := upstream.New(t)
	payload := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`

	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Fatalf("第一发就该是好的")
	}
	s.FailNext(http.StatusServiceUnavailable)
	code, raw := post(t, s, "/v1/chat/completions", payload)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("武装之后的第一发 = %d，期望 503（体：%s）", code, raw)
	}
	if !strings.Contains(raw, "mock forced 503") {
		t.Errorf("失败响应体 = %s，期望带 mock forced 503", raw)
	}
	if code, _ = post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Fatalf("用完即消：第二发 = %d，期望 200", code)
	}
	// 失败那一发也留在记录里（先记后判），所以能断言「网关确实重试了」
	if s.Count() != 3 {
		t.Errorf("记录数 = %d，期望 3（含那一发失败的）", s.Count())
	}

	// FailNext(0) 等于解除武装（python 里 0 是 falsy）
	s.FailNext(http.StatusTooManyRequests)
	s.FailNext(0)
	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Errorf("FailNext(0) 之后 = %d，期望 200（0 是解除武装）", code)
	}

	// 失败注入也能落在 count_tokens 上：python 判失败在判 count_tokens 之前
	s.FailNext(http.StatusTooManyRequests)
	if code, _ := post(t, s, "/v1/messages/count_tokens", `{"messages":[]}`); code != http.StatusTooManyRequests {
		t.Errorf("武装中的 count_tokens = %d，期望 429", code)
	}
}

func TestRequestsAndReset(t *testing.T) {
	s := upstream.New(t)
	first := `{"model":"deepseek-chat","messages":[{"role":"user","content":"1"}]}`
	second := `{"model":"glm-4-plus","messages":[{"role":"user","content":"2"}]}`

	if code, raw := post(t, s, "/v1/chat/completions", first); code != http.StatusOK {
		t.Fatalf("第一发 = %d（%s）", code, raw)
	}
	if code, raw := post(t, s, "/v1/messages", second); code != http.StatusOK {
		t.Fatalf("第二发 = %d（%s）", code, raw)
	}

	records := s.Requests()
	if len(records) != 2 {
		t.Fatalf("记录数 = %d，期望 2", len(records))
	}
	if records[0].Path != "/v1/chat/completions" || records[1].Path != "/v1/messages" {
		t.Errorf("记录顺序不对：%q, %q（要按时间顺序）", records[0].Path, records[1].Path)
	}
	if string(records[0].Body) != first || string(records[1].Body) != second {
		t.Errorf("请求体记错了：%q, %q", records[0].Body, records[1].Body)
	}
	if records[1].At.Before(records[0].At) {
		t.Errorf("时间戳没有递增：%v → %v", records[0].At, records[1].At)
	}

	// 返回的是副本：改它不能穿透到 Server 内部
	records[0].Path = "改着玩"
	if s.Requests()[0].Path != "/v1/chat/completions" {
		t.Error("Requests() 返回的该是副本，改它不该影响内部记录")
	}

	s.Reset()
	if s.Count() != 0 || len(s.Requests()) != 0 {
		t.Fatalf("Reset 之后还有记录：Count=%d", s.Count())
	}

	// Reset 也解除 FailNext 的武装（python 的 /__mock/reset 同时清 NEXT_FAIL）
	s.FailNext(http.StatusInternalServerError)
	s.Reset()
	if code, raw := post(t, s, "/v1/chat/completions", first); code != http.StatusOK {
		t.Errorf("Reset 之后 = %d，期望 200（Reset 该顺手解除武装）（体：%s）", code, raw)
	}
}

// TestControlPlaneHTTP 走 HTTP 面的控制端点——这条路径是 bash 脚本和 curl 用的，
// 形状必须与 python 版一字不变（小写头、解析后的 body、epoch 秒、空记录是 []）。
func TestControlPlaneHTTP(t *testing.T) {
	s := upstream.New(t)

	code, raw := get(t, s, "/v1/models")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/models = %d", code)
	}
	models := decodeObj(t, raw)
	if models["object"] != "list" {
		t.Errorf("object = %v", models["object"])
	}
	if data, ok := models["data"].([]any); !ok || len(data) != 0 {
		t.Errorf("data = %v，期望空数组", models["data"])
	}

	if code, _ := get(t, s, "/nope"); code != http.StatusNotFound {
		t.Errorf("未知 GET 路径 = %d，期望 404", code)
	}

	payload := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	resp := send(t, s, http.MethodPost, "/v1/chat/completions", payload, map[string]string{
		"Authorization": "Bearer sk-ds",
	})
	resp.Body.Close()

	code, raw = get(t, s, "/__mock/requests")
	if code != http.StatusOK {
		t.Fatalf("GET /__mock/requests = %d", code)
	}
	var wire []map[string]any
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("记录不是 JSON 数组: %v（%s）", err, raw)
	}
	if len(wire) != 1 {
		t.Fatalf("记录数 = %d，期望 1（%s）", len(wire), raw)
	}
	if got := wire[0]["path"]; got != "/v1/chat/completions" {
		t.Errorf("wire path = %v", got)
	}
	if got := wire[0]["method"]; got != "POST" {
		t.Errorf("wire method = %v", got)
	}
	headers, ok := wire[0]["headers"].(map[string]any)
	if !ok {
		t.Fatalf("wire headers 不是对象: %v", wire[0]["headers"])
	}
	if got := headers["authorization"]; got != "Bearer sk-ds" {
		t.Errorf("wire headers[authorization] = %v（python 版是小写键）", got)
	}
	if _, mixed := headers["Authorization"]; mixed {
		t.Error("wire headers 里不该出现原大小写的键")
	}
	if got := dig(t, wire[0], "body", "model"); got != "deepseek-chat" {
		t.Errorf("wire body.model = %v（body 该是解析后的对象）", got)
	}
	at, ok := wire[0]["at"].(float64)
	if !ok || at <= 0 {
		t.Errorf("wire at = %v，期望 epoch 秒", wire[0]["at"])
	}

	// 空记录必须是 []，不是 null：bash 里的 for x in r 遇到 null 会当场炸
	s.Reset()
	if _, raw = get(t, s, "/__mock/requests"); strings.TrimSpace(raw) != "[]" {
		t.Errorf("空记录 = %q，期望 []", strings.TrimSpace(raw))
	}

	// GET /__mock/fail?code=N 武装下一发
	code, raw = get(t, s, "/__mock/fail?code=429")
	if code != http.StatusOK {
		t.Fatalf("GET /__mock/fail = %d", code)
	}
	if got := decodeObj(t, raw)["next_fail"]; got != float64(429) {
		t.Errorf("next_fail = %v，期望 429", got)
	}
	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusTooManyRequests {
		t.Errorf("武装后第一发 = %d，期望 429", code)
	}
	if code, _ := post(t, s, "/v1/chat/completions", payload); code != http.StatusOK {
		t.Errorf("武装后第二发 = %d，期望 200（用完即消）", code)
	}
	// 缺省 code=500（python 的 q.get("code", ["500"])）
	if _, raw = get(t, s, "/__mock/fail"); decodeObj(t, raw)["next_fail"] != float64(500) {
		t.Errorf("缺省 code 该是 500：%s", raw)
	}
	// 不是整数：python 会抛异常断连，这里明说 400（有意的分歧）
	if code, _ := get(t, s, "/__mock/fail?code=abc"); code != http.StatusBadRequest {
		t.Errorf("code=abc 的状态码 = %d，期望 400", code)
	}
	s.FailNext(0) // 别把上面那发缺省 500 留给后面的用例

	// POST /__mock/reset 清记录，且这一发不进记录表（python 也在读 body 之前处理）
	if code, raw = post(t, s, "/__mock/reset", ""); code != http.StatusOK {
		t.Fatalf("POST /__mock/reset = %d（%s）", code, raw)
	}
	if s.Count() != 0 {
		t.Errorf("reset 之后记录数 = %d", s.Count())
	}
}

// ---- 延迟 ----

// TestStreamIsIncremental 验证两件事：块间隔真的生效、而且响应是逐块出来的
// （代理要是把整条流缓冲下来，首块也得等全吐完才到——这条断言就是冲着它）。
func TestStreamIsIncremental(t *testing.T) {
	s := upstream.New(t)
	const delay = 200 * time.Millisecond // 4 个增量块 → 整条流 ~800ms

	// 先把基准延迟调快：mock_slow 走的是另一档，应当不受影响
	s.SetChunkDelay(0)
	s.SetSlowChunkDelay(delay)

	start := time.Now()
	code, raw := post(t, s, "/v1/chat/completions",
		`{"model":"m","stream":true,"mock_slow":true,"messages":[]}`)
	elapsed := time.Since(start)
	if code != http.StatusOK {
		t.Fatalf("mock_slow 流 = %d（%s）", code, raw)
	}
	if elapsed < 3*delay {
		t.Errorf("mock_slow 的流只花了 %v（期望 >= %v，约 4 个块间隔）——慢档没生效？", elapsed, 3*delay)
	}
	if !strings.HasSuffix(raw, "data: [DONE]\n\n") {
		t.Errorf("流没完整吐完，尾部是：%q", raw[len(raw)-40:])
	}

	// 基准延迟：逐块到达 + 整条流的时长都对得上
	s.SetChunkDelay(delay)
	resp := send(t, s, http.MethodPost, "/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[]}`, nil)
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	start = time.Now()
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("读首块: %v", err)
	}
	first := time.Since(start)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("读余下的流: %v", err)
	}
	total := time.Since(start)

	// 缓冲整条流的实现要 ~800ms 才给第一块；阈值放到 400ms：机器抖动不至于
	// 假失败，而「攒着发」一定超。
	if first > 400*time.Millisecond {
		t.Errorf("首块 %v 才到（整条流约 %v）——流式没有逐块吐？", first, 4*delay)
	}
	if total < 3*delay {
		t.Errorf("整条流只花了 %v（期望 >= %v）——块间隔没生效？", total, 3*delay)
	}
	stream := firstLine + string(rest)
	if got := strings.Count(stream, "data: "); got != 6 {
		t.Errorf("data: 行数 = %d，期望 6（4 增量 + 1 收尾 + [DONE]）", got)
	}
	if !strings.HasSuffix(stream, "data: [DONE]\n\n") {
		t.Errorf("流没完整吐完，尾部是：%q", stream[len(stream)-40:])
	}

	// SetChunkDelay(0) = 尽快：同一发立刻回来（延迟是可调的，不是写死的）
	s.SetChunkDelay(0)
	start = time.Now()
	if code, raw := post(t, s, "/v1/chat/completions", `{"model":"m","stream":true,"messages":[]}`); code != http.StatusOK {
		t.Fatalf("SetChunkDelay(0) 之后 = %d（%s）", code, raw)
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("SetChunkDelay(0) 之后还花了 %v，期望尽快", elapsed)
	}
}
