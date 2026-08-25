package thinkcache

import (
	"strings"
	"testing"
	"time"
)

const reasoning = "先看 \"main.go\"\n再看 src\\a"

// SSE 的事件边界和 TCP 的读边界毫无关系：一段推理完全可能被切在
// `reasoning_` 和 `content` 之间。逐字节喂是最狠的边界测试。
func TestObserverSurvivesArbitraryChunking(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"reasoning_content":"先看 \"main.go\"\n"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_content":"再看 src\\a"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"好的"},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x"}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"

	for _, size := range []int{1, 3, 17, 4096} {
		o := NewObserver()
		for i := 0; i < len(stream); i += size {
			j := i + size
			if j > len(stream) {
				j = len(stream)
			}
			o.Write([]byte(stream[i:j]))
		}
		if got := o.Reasoning(); got != reasoning {
			t.Fatalf("分块 %d 字节时推理内容不对\n want %q\n got  %q", size, reasoning, got)
		}
		keys := o.Keys()
		if len(keys) != 2 || keys[0] != ToolKey("call_x") {
			t.Fatalf("分块 %d 字节时 key 不对: %v", size, keys)
		}
	}
}

// Anthropic 方言：thinking_delta / content_block_start(tool_use)
func TestObserverAnthropicDialect(t *testing.T) {
	o := NewObserver()
	for _, ev := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先看 \"main.go\"\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"再看 src\\a"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"Read"}}`,
	} {
		o.Write([]byte("event: x\ndata: " + ev + "\n\n"))
	}
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q, got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("toolu_9") {
		t.Fatalf("key 不对: %v", k)
	}
}

// 响应侧挂的 key 和请求侧找的 key 必须严格对应，改一边就得改另一边。
func TestKeysMirrorBetweenResponseAndRequest(t *testing.T) {
	cases := []struct {
		name     string
		respTool string
		respText string
		reqItem  string
	}{
		{"OpenAI 方言 tool_call", "call_x", "",
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_x"}]}`},
		{"Anthropic 方言 tool_use", "toolu_9", "",
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_9"}]}`},
		{"纯文本轮（OpenAI）", "", "好的，我看完了",
			`{"role":"assistant","content":"好的，我看完了"}`},
		{"纯文本轮（Anthropic）", "", "好的，我看完了",
			`{"role":"assistant","content":[{"type":"text","text":"好的，我看完了"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(1<<20, time.Hour)
			o := NewObserver()
			o.reason.WriteString(reasoning)
			o.text.WriteString(tc.respText)
			if tc.respTool != "" {
				o.addTool(tc.respTool)
			}
			if n, _ := o.Commit(c); n == 0 {
				t.Fatal("没存进去")
			}
			blob, ok := c.Lookup([]byte(tc.reqItem))
			if !ok {
				t.Fatalf("请求侧找不回来。响应侧 key=%v，请求侧 key=%v",
					o.Keys(), KeysForAssistantMessage([]byte(tc.reqItem)))
			}
			if string(blob) != reasoning {
				t.Fatalf("内容不一致: %q", blob)
			}
		})
	}
}

// 一轮多个 tool_call：认出任意一个 id 都要能找回同一段推理。
func TestAnyToolIDFindsTheSameReasoning(t *testing.T) {
	c := New(1<<20, time.Hour)
	c.Put([]string{ToolKey("a"), ToolKey("b"), ToolKey("c")}, []byte(reasoning))
	for _, id := range []string{"a", "b", "c"} {
		item := `{"role":"assistant","tool_calls":[{"id":"` + id + `"}]}`
		if blob, ok := c.Lookup([]byte(item)); !ok || string(blob) != reasoning {
			t.Fatalf("id=%s 找不回来", id)
		}
	}
}

func TestTTLAndByteCap(t *testing.T) {
	c := New(1<<20, 20*time.Millisecond)
	c.Put([]string{"k"}, []byte(reasoning))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("刚存进去就没了")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("过了 TTL 还在")
	}

	// 字节封顶：塞超量后总字节数不许越界，最早的被淘汰
	small := New(300, time.Hour)
	blob := []byte(strings.Repeat("x", 100))
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		small.Put([]string{k}, blob)
	}
	st := small.Stats()
	if st.Bytes > st.MaxBytes {
		t.Fatalf("超出上限: %d > %d", st.Bytes, st.MaxBytes)
	}
	if _, ok := small.Get("a"); ok {
		t.Fatal("最早的没被淘汰")
	}
	if _, ok := small.Get("e"); !ok {
		t.Fatal("最新的被淘汰了")
	}
}

// 没有能对上号的 key 就别存——存了也永远找不回来，白占地方。
func TestNoKeysNoStore(t *testing.T) {
	c := New(1<<20, time.Hour)
	o := NewObserver()
	o.reason.WriteString(reasoning)
	if n, k := o.Commit(c); n != 0 || k != 0 {
		t.Fatalf("没有 key 也存了: %d 字节 / %d key", n, k)
	}
	if c.Stats().Entries != 0 {
		t.Fatal("缓存里多了东西")
	}
}

// 非流式响应也要能观测到。
func TestObserveBody(t *testing.T) {
	o := NewObserver()
	o.ObserveBody([]byte(`{"choices":[{"message":{"role":"assistant",` +
		`"reasoning_content":"先看 \"main.go\"\n再看 src\\a","content":"好",` +
		`"tool_calls":[{"id":"call_z"}]}}]}`))
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("call_z") {
		t.Fatalf("key 不对: %v", k)
	}
}
