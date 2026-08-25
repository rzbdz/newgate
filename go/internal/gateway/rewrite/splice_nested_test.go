package rewrite

import (
	"encoding/json"
	"strings"
	"testing"
)

// 插入点必须在子数组**开头**，且除了插进去的那段，其余每个字节原样保留。
func TestEnsureArrayItemArrayHead(t *testing.T) {
	body := []byte("{\n  \"messages\": [\n" +
		"    {\"role\": \"user\", \"content\": [{\"type\":\"text\",\"text\":\"a\"}]},\n" +
		"    {\"role\": \"assistant\", \"content\": [{\"type\":\"text\",\"text\":\"b\"}]}\n" +
		"  ],\n  \"max_tokens\": 1e10\n}")

	isAsst := func(item []byte) bool { r, _ := TopLevelString(item, "role"); return r == "assistant" }

	out, n, err := EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking","thinking":""}`), isAsst, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Fatalf("插了 %d 条，想要 1", n)
	}
	if !json.Valid(out) {
		t.Fatalf("结果不是合法 JSON:\n%s", out)
	}
	if !strings.Contains(string(out),
		`"content": [{"type":"thinking","thinking":""},{"type":"text","text":"b"}]`) {
		t.Fatalf("没插在开头:\n%s", out)
	}
	// user 那条与顶层其余字节不动：缩进、key 顺序、1e10 的字面量都在
	if !strings.Contains(string(out), `{"role": "user", "content": [{"type":"text","text":"a"}]}`) {
		t.Fatalf("user 消息被动了:\n%s", out)
	}
	if !strings.Contains(string(out), `"max_tokens": 1e10`) {
		t.Fatalf("数字字面量被改写了:\n%s", out)
	}
}

// 空数组不需要补逗号；need 返回 false 的一律不碰，且返回原字节。
func TestEnsureArrayItemArrayHeadEdges(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[]}]}`)
	out, n, err := EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking"}`), nil, nil)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if string(out) != `{"messages":[{"role":"assistant","content":[{"type":"thinking"}]}]}` {
		t.Fatalf("空数组插错了:\n%s", out)
	}

	never := func([]byte) bool { return false }
	out, n, err = EnsureArrayItemArrayHead(body, "messages", "content",
		[]byte(`{"type":"thinking"}`), nil, never)
	if err != nil || n != 0 || string(out) != string(body) {
		t.Fatalf("need=false 时不该动: n=%d err=%v\n%s", n, err, out)
	}
}

func TestArrayItems(t *testing.T) {
	items, ok := ArrayItems([]byte(`[ {"a":[1,2]} , "s" , 3 ]`))
	if !ok {
		t.Fatal("扫不动")
	}
	want := []string{`{"a":[1,2]}`, `"s"`, `3`}
	if len(items) != len(want) {
		t.Fatalf("切出 %d 个，想要 %d", len(items), len(want))
	}
	for i, w := range want {
		if string(items[i]) != w {
			t.Fatalf("第 %d 个 = %s，想要 %s", i, items[i], w)
		}
	}
	if _, ok := ArrayItems([]byte(`{"not":"array"}`)); ok {
		t.Fatal("对象被当成数组了")
	}
}
