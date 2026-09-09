package forward

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
)

// TestCompactKeepsExplicitThinking 端到端回归（2026-09-09 实抓 + 探针复现）。
//
// /compact 总结请求的真实形态（dump req-000138 复核）是：
//
//	heavy + stream=false + thinking:{"type":"adaptive"} + tools + max_tokens 32000
//
// claude-bg 曾把它当「没写 thinking 的后台调用」，强改 thinking:disabled。
// 对始终思考的 glm-5.3（code 1210）那是唯一触发 400 的形态——上游只看到
// 输入、吐不出任何输出，客户端等到「client gone」或「Streaming response
// ended before any complete data」。
//
// 修后：显式带 adaptive 的请求一个字节不碰；disabled 只补在「真没写
// thinking」的请求上（分类器/起标题那类）。本测试锁住前者不回归。
func TestCompactKeepsExplicitThinking(t *testing.T) {
	var sent []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = ioutil.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "glm", Model: "glm-5.3"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// /a/claude/ 让 Agent=claude（claude-bg 只认这个 agent，dump 里 compact
	// 就是 claude 发起的非流式）。
	compact := `{"model":"heavy","stream":false,"max_tokens":32000,` +
		`"thinking":{"type":"adaptive"},` +
		`"tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"summarize this long transcript"}]}`
	resp, err := http.Post(front.URL+"/a/claude/v1/messages", "application/json",
		strings.NewReader(compact))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(resp.Body)
		t.Fatalf("状态 %d: %s", resp.StatusCode, b)
	}
	if len(sent) == 0 {
		t.Fatal("上游没收到请求体")
	}
	var got struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(sent, &got); err != nil {
		t.Fatalf("发出去的不是合法 JSON: %v\n%s", err, sent)
	}
	if got.Thinking.Type != "adaptive" {
		t.Errorf("compact 显式 thinking:adaptive 不该被强改成 disabled，实际 %q\n%s",
			got.Thinking.Type, sent)
	}
	if len(got.Tools) == 0 {
		t.Errorf("tools 不该被摘掉: %s", sent)
	}
}

// TestBackgroundCallStillDisablesThinking 对照组：真没写 thinking 的后台调用
// （分类器/起标题形态）仍补 disabled——修的是「强改显式意图」，不是「放弃
// 禁思考」这条 intent。
func TestBackgroundCallStillDisablesThinking(t *testing.T) {
	var sent []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = ioutil.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "glm", Model: "glm-5.3-flash"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// 没写 thinking、没写 tools 的非流式后台调用
	bg := `{"model":"mid","max_tokens":2112,` +
		`"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}],` +
		`"messages":[{"role":"user","content":"classify"}]}`
	resp, err := http.Post(front.URL+"/a/claude/v1/messages", "application/json",
		strings.NewReader(bg))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = ioutil.ReadAll(resp.Body)

	var got struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(sent, &got); err != nil {
		t.Fatalf("发出去的不是合法 JSON: %v\n%s", err, sent)
	}
	if got.Thinking.Type != "disabled" {
		t.Errorf("没写 thinking 的后台调用仍应补 disabled，实际 %q", got.Thinking.Type)
	}
}
