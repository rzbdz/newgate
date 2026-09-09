package forward

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/gateway/dialect"
)

// midChainOnly 测试桩：mid 档指向 given 上游，其他档为空——count_tokens
// 的转发只看 mid 链头（主循环在 mid 跑）。
func midChainOnly(t *testing.T, up *httptest.Server) {
	t.Helper()
	dialect.Reset()
	t.Cleanup(func() {
		dialect.Reset()
		testChain = nil
	})
	testChain = func(role string) []resolve.Step {
		if role != "mid" {
			return nil
		}
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(up.URL),
		}}
	}
}

// ctPost 经代理发一个 count_tokens 请求（不带 model 字段——正是日志里
// 「请求体顶层没有 model 字符串字段」的形态）。
func ctPost(t *testing.T, front *httptest.Server, path string) (int, []byte) {
	t.Helper()
	body := `{"messages":[{"role":"user","content":"看下这个，顺便数数 token"}],"tools":[]}`
	resp, err := http.Post(front.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, b
}

// TestCountTokensLazyProbeThenLocal 上游没有 count_tokens（聚合器实测
// 形态：/messages 200、/messages/count_tokens 404）。gate 层面的 lazy
// probe：第一发试转发、撞 404、学到「没有」退回本地粗估；此后不再白跑
// （上游零命中），客户端拿到的始终是本地 200。
func TestCountTokensLazyProbeThenLocal(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"Invalid URL"}`))
	}))
	defer up.Close()
	midChainOnly(t, up)

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for i, path := range []string{
		"/v1/messages/count_tokens",
		"/a/claude/p/ds/v1/messages/count_tokens",
		"/v1/messages/count_tokens", // 第三发：已学到，不该再撞
	} {
		code, b := ctPost(t, front, path)
		if code != 200 {
			t.Fatalf("第 %d 发 %s: 客户端必须拿到 200（本地粗估兜底），实际 %d: %s",
				i+1, path, code, b)
		}
		var got struct {
			InputTokens int `json:"input_tokens"`
		}
		if err := json.Unmarshal(b, &got); err != nil || got.InputTokens <= 0 {
			t.Fatalf("第 %d 发: 应答不是 {input_tokens:N>0}: %s", i+1, b)
		}
	}
	if hits != 1 {
		t.Fatalf("上游应只被撞 1 次（lazy probe），实际 %d 次——学到的「不支持」没生效", hits)
	}
	// 学到的状态本身也要对
	if ok, known := dialect.Supports("test-prov", "real-model-1", dialect.CapCountTokens); !known || ok {
		t.Fatalf("应已学到明确不支持，实际 (ok=%v known=%v)", ok, known)
	}
}

// TestCountTokensForwardedWhenSupported 上游有 count_tokens（原生
// anthropic 端点）：转发拿真值，model 字段按 mid 链头补上。
func TestCountTokensForwardedWhenSupported(t *testing.T) {
	var gotModel, gotPath, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var m map[string]json.RawMessage
		b, _ := ioutil.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		_ = json.Unmarshal(m["model"], &gotModel)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer up.Close()
	midChainOnly(t, up)

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	code, b := ctPost(t, front, "/v1/messages/count_tokens")
	if code != 200 {
		t.Fatalf("应转发上游 200，实际 %d: %s", code, b)
	}
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.InputTokens != 42 {
		t.Fatalf("应拿上游真值 42，实际 %s", b)
	}
	if gotPath != "/v1/messages/count_tokens" {
		t.Errorf("上游路径 = %s", gotPath)
	}
	if gotModel != "real-model-1" {
		t.Errorf("model 应按 mid 链头补上，实际 %q", gotModel)
	}
	if gotAuth != "Bearer sk-real" {
		t.Errorf("auth 跟 provider 声明的协议走，实际 %q", gotAuth)
	}

	// 学到「支持」后再来一发，仍是真值
	code, b = ctPost(t, front, "/v1/messages/count_tokens")
	if code != 200 {
		t.Fatalf("第二发应继续转发，实际 %d", code)
	}
	if !strings.Contains(string(b), "42") {
		t.Fatalf("第二发仍是真值，实际 %s", b)
	}
}

// TestCountTokensTransientErrorNotLearned 429 是「现在不行」不是「没有」：
// 不学，本次退回本地粗估；下一发继续试转发。
func TestCountTokensTransientErrorNotLearned(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer up.Close()
	midChainOnly(t, up)

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for i := 0; i < 2; i++ {
		code, b := ctPost(t, front, "/v1/messages/count_tokens")
		if code != 200 {
			t.Fatalf("第 %d 发: 429 应退回本地 200，实际 %d: %s", i+1, code, b)
		}
	}
	if hits != 2 {
		t.Fatalf("429 不该被学成「不支持」，两发都应试过转发，实际命中 %d 次", hits)
	}
	if _, known := dialect.Supports("test-prov", "real-model-1", dialect.CapCountTokens); known {
		t.Fatal("429 不该学任何状态")
	}
}

// TestCountTokensAlreadyLearnedSkipsUpstream 已探明支持的上游直接转发、
// 不再走本地粗估（对照：LazyProbe 用例里已探明不支持的直接本地）。
func TestCountTokensAlreadyLearnedSkipsUpstream(t *testing.T) {
	dialect.Reset()
	t.Cleanup(dialect.Reset)
	t.Cleanup(func() { testChain = nil })

	// 上游挂了（连接被拒也无所谓——根本不该连它）
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("已学到「不支持」，不该再碰上游")
	}))
	upURL := up.URL
	up.Close() // 故意关掉：连不上也不影响本地粗估

	testChain = func(role string) []resolve.Step {
		if role != "mid" {
			return nil
		}
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: domain.Provider{BaseURL: upURL + "/v1", APIKey: "sk-real", Protocol: "openai"},
		}}
	}
	dialect.MarkUnsupported("test-prov", "real-model-1", dialect.CapCountTokens)

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	code, b := ctPost(t, front, "/v1/messages/count_tokens")
	if code != 200 {
		t.Fatalf("应本地 200，实际 %d: %s", code, b)
	}
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.InputTokens <= 0 {
		t.Fatalf("本地粗估应答不对: %s", b)
	}
}

// TestCountTokensRespectsClientModel 客户端自己带了 model 字段就尊重它，
// 不拿 mid 链头覆盖（count_tokens 按点名模型的 tokenizer 算才对）。
func TestCountTokensRespectsClientModel(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]json.RawMessage
		b, _ := ioutil.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		_ = json.Unmarshal(m["model"], &gotModel)
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer up.Close()
	midChainOnly(t, up)

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	body := `{"model":"glm-4.5-air","messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(front.URL+"/v1/messages/count_tokens",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "7") {
		t.Fatalf("应转发拿真值: %d %s", resp.StatusCode, b)
	}
	if gotModel != "glm-4.5-air" {
		t.Fatalf("客户端的 model 不该被覆盖，上游收到 %q", gotModel)
	}
}
