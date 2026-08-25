package forward

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/gateway/thinkcache"
	"github.com/rzbdz/newgate/go/internal/store"
)

// 故意难搞的推理内容：换行、双引号、反斜杠、中文、大整数。
// 逐字回传是 DeepSeek 的硬要求，任何一个字符对不上都算失败。
const wantReasoning = "用户想让我读文件。\n先看 \"main.go\"，路径是 C:\\src\\a。\n" +
	"注意 id=9007199254740993 不能被改成 ...92。"

// TestReasoningRoundTripThroughDroppingClient 是这套机制的验收测试。
//
// 演的就是线上那出戏：
//  1. 上游流式吐出 reasoning_content + 一个 tool_call
//  2. 客户端（模拟 opencode / Claude Code 的行为）把 reasoning_content **丢掉**，
//     只把 assistant 消息和 tool_calls 塞回历史
//  3. 第二轮请求经过 newgate 时，我们必须把那段推理**逐字**补回去
//
// 补不回去就是线上现在的状态：要么 400，要么补个空串把模型的思考搞成傻子。
func TestReasoningRoundTripThroughDroppingClient(t *testing.T) {
	isolate(t)

	var mu sync.Mutex
	turn := 0
	var sawReasoning string
	var sawTurn2 bool

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := ioutil.ReadAll(r.Body)
		mu.Lock()
		turn++
		n := turn
		mu.Unlock()

		if n == 1 {
			// 第一轮：流式吐推理内容 + tool_call。
			// 故意把一段推理切在诡异的位置，逼观测者自己找 SSE 事件边界。
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl, _ := w.(http.Flusher)
			mid := len(wantReasoning) / 2
			for _, p := range splitAtRunes(wantReasoning, mid) {
				blob, _ := json.Marshal(p)
				_, _ = io.WriteString(w,
					`data: {"choices":[{"delta":{"reasoning_content":`+string(blob)+`}}]}`+"\n\n")
				if fl != nil {
					fl.Flush()
				}
			}
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":`+
				`[{"index":0,"id":"call_abc123","function":{"name":"Read","arguments":"{}"}}]}}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			if fl != nil {
				fl.Flush()
			}
			return
		}

		// 第二轮：断言我们替客户端把推理补回来了
		mu.Lock()
		sawTurn2 = true
		mu.Unlock()
		var req struct {
			Messages []struct {
				Role             string `json:"role"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(reqBody, &req); err != nil {
			t.Errorf("第二轮请求体不是合法 JSON: %v", err)
		}
		for _, m := range req.Messages {
			if m.Role == "assistant" {
				mu.Lock()
				sawReasoning = m.ReasoningContent
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer up.Close()

	// provider 名里带 deepseek，插件才会认领这次请求
	testChain = func(tier string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "smt-deepseek", Model: "deepseek-v4-pro"},
			Provider: domain.Provider{BaseURL: up.URL + "/v1", APIKey: "sk-real", Protocol: "openai"},
		}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// ---- 第一轮：正常问一句，把流读完（读完才会 Commit 进缓存）----
	turn1 := `{"model":"newgate/heavy","stream":true,` +
		`"tools":[{"type":"function","function":{"name":"Read"}}],` +
		`"messages":[{"role":"user","content":"读一下 main.go"}]}`
	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(turn1))
	if err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	body1, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("第一轮状态 %d: %s", resp.StatusCode, body1)
	}
	// 转发必须字节保真：我们只是旁路看了一眼，不许改流的内容
	if !strings.Contains(string(body1), `"reasoning_content"`) {
		t.Fatalf("第一轮的流被动过了，客户端看不到 reasoning_content:\n%s", body1)
	}

	// ---- 第二轮：模拟一个把 reasoning_content 丢掉的客户端 ----
	turn2 := `{"model":"newgate/heavy","stream":false,` +
		`"tools":[{"type":"function","function":{"name":"Read"}}],` +
		`"messages":[` +
		`{"role":"user","content":"读一下 main.go"},` +
		`{"role":"assistant","content":"",` +
		`"tool_calls":[{"id":"call_abc123","type":"function",` +
		`"function":{"name":"Read","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_abc123","content":"package main"}` +
		`]}`
	resp2, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(turn2))
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	b2, _ := ioutil.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("第二轮状态 %d: %s", resp2.StatusCode, b2)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawTurn2 {
		t.Fatal("第二轮没打到上游")
	}
	if sawReasoning == "" {
		t.Fatal("上游收到的 assistant 消息里 reasoning_content 是空的 —— " +
			"没补回来，模型这一轮看不到自己上一轮的推理")
	}
	if sawReasoning != wantReasoning {
		t.Fatalf("补回来的推理内容和原文不一致（DeepSeek 要求逐字）\n want: %q\n got:  %q",
			wantReasoning, sawReasoning)
	}
	t.Logf("客户端丢掉的 %d 字节推理内容已逐字补回", len(sawReasoning))
}

// splitAtRunes 在不切坏 UTF-8 的前提下把字符串切两半。
func splitAtRunes(s string, at int) []string {
	r := []rune(s)
	n := len(r) * at / len(s)
	if n <= 0 || n >= len(r) {
		n = len(r) / 2
	}
	return []string{string(r[:n]), string(r[n:])}
}

// isolate 把配置目录指到临时目录，免得测试读到（或写坏）真实的 ~/.config/newgate。
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", dir)
	t.Setenv("HOME", dir)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("初始化临时配置失败: %v", err)
	}
	thinkcache.Default = thinkcache.New(32<<20, 0) // 每个测试从空缓存开始
}

// TestThinkingBlockRoundTripAnthropicDialect 同一件事的 Anthropic 方言版：
// 客户端（Claude Code 对非官方端点必然如此）把 content[] 里的 thinking 块
// 剥掉，我们要把它原样插回 content[] 的**开头**。
func TestThinkingBlockRoundTripAnthropicDialect(t *testing.T) {
	isolate(t)

	var mu sync.Mutex
	turn := 0
	var sawThinking string
	var firstBlockType string

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := ioutil.ReadAll(r.Body)
		mu.Lock()
		turn++
		n := turn
		mu.Unlock()

		if n == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl, _ := w.(http.Flusher)
			blob, _ := json.Marshal(wantReasoning)
			for _, ev := range []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":` + string(blob) + `}}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_77","name":"Read"}}`,
				`{"type":"message_stop"}`,
			} {
				_, _ = io.WriteString(w, "data: "+ev+"\n\n")
				if fl != nil {
					fl.Flush()
				}
			}
			return
		}

		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type     string `json:"type"`
					Thinking string `json:"thinking"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(reqBody, &req); err != nil {
			t.Errorf("第二轮请求体不是合法 JSON: %v", err)
		}
		for _, m := range req.Messages {
			if m.Role != "assistant" || len(m.Content) == 0 {
				continue
			}
			mu.Lock()
			firstBlockType = m.Content[0].Type
			sawThinking = m.Content[0].Thinking
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"done"}]}`)
	}))
	defer up.Close()

	testChain = func(tier string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "smt-deepseek", Model: "deepseek-v4-pro"},
			Provider: domain.Provider{BaseURL: up.URL + "/v1", APIKey: "sk-real", Protocol: "anthropic"},
		}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	turn1 := `{"model":"newgate/heavy","stream":true,"thinking":{"type":"enabled","budget_tokens":1024},` +
		`"tools":[{"name":"Read"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"读一下 main.go"}]}]}`
	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(turn1))
	if err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	b1, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("第一轮状态 %d: %s", resp.StatusCode, b1)
	}

	// 客户端把 thinking 块剥掉了，只剩 tool_use
	turn2 := `{"model":"newgate/heavy","stream":false,"thinking":{"type":"enabled","budget_tokens":1024},` +
		`"tools":[{"name":"Read"}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"读一下 main.go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_77","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_77","content":"package main"}]}` +
		`]}`
	resp2, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(turn2))
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	b2, _ := ioutil.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("第二轮状态 %d: %s", resp2.StatusCode, b2)
	}

	mu.Lock()
	defer mu.Unlock()
	if firstBlockType != "thinking" {
		t.Fatalf("content[] 第一个块是 %q，协议要求 thinking 排在 tool_use 之前", firstBlockType)
	}
	if sawThinking != wantReasoning {
		t.Fatalf("thinking 块内容和原文不一致\n want: %q\n got:  %q", wantReasoning, sawThinking)
	}
	t.Logf("Anthropic 方言：%d 字节 thinking 块已逐字补回 content[] 开头", len(sawThinking))
}
