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

// TestSpecialTreatmentDeepseekOnTheWire 端到端断言：转给 DeepSeek 的请求里，
// thinking 已被关掉、每条 assistant 消息都带上了 reasoning_content——
// 这就是 400 `The reasoning_content in the thinking mode must be passed back
// to the API` 的修法。同时断言客户端原本的字节（cache_control、大整数、
// 中文）一个不动。
func TestSpecialTreatmentDeepseekOnTheWire(t *testing.T) {
	var sent []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = ioutil.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "ds", Model: "deepseek-chat"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	clientBody := `{"model":"newgate/heavy","max_tokens":9007199254740993,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"看下这个","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Read"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01"}]},` +
		`{"role":"assistant","content":"再来一轮"}` +
		`]}`
	resp, err := http.Post(front.URL+"/v1/messages", "application/json",
		strings.NewReader(clientBody))
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
		Model    string `json:"model"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(sent, &got); err != nil {
		t.Fatalf("我们发出去的不是合法 JSON: %v\n%s", err, sent)
	}
	if got.Model != "deepseek-chat" {
		t.Errorf("model 没被改写成真实模型名: %q", got.Model)
	}
	if got.Thinking.Type != "disabled" {
		t.Errorf(`thinking 没被关掉: %s`, sent)
	}
	assistants := 0
	for i, m := range got.Messages {
		var role string
		_ = json.Unmarshal(m["role"], &role)
		if role != "assistant" {
			if _, has := m["reasoning_content"]; has {
				t.Errorf("第 %d 条 %s 被误补了 reasoning_content", i, role)
			}
			continue
		}
		assistants++
		if rc, has := m["reasoning_content"]; !has || string(rc) != `""` {
			t.Errorf("第 %d 条 assistant 缺 reasoning_content: %s", i, sent)
		}
	}
	if assistants != 2 {
		t.Errorf("应该有 2 条 assistant，实际 %d", assistants)
	}

	// 客户端语义必须逐字节存活
	for _, want := range []string{
		`"cache_control":{"type":"ephemeral"}`,
		`"max_tokens":9007199254740993`, // JSON 往返会把它变成 ...92
		`"text":"看下这个"`,
	} {
		if !strings.Contains(string(sent), want) {
			t.Errorf("原始字节没保住: 缺 %s\n实际: %s", want, sent)
		}
	}
}

// TestSpecialTreatmentSkipsNonDeepseek 非 DeepSeek 上游一个字节都不该多。
func TestSpecialTreatmentSkipsNonDeepseek(t *testing.T) {
	var sent []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = ioutil.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "anth", Model: "claude-sonnet-4"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"assistant","content":"a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = ioutil.ReadAll(resp.Body)

	want := `{"model":"claude-sonnet-4","messages":[{"role":"assistant","content":"a"}]}`
	if string(sent) != want {
		t.Errorf("非 DeepSeek 上游的请求被动了\n want: %s\n got:  %s", want, sent)
	}
}
