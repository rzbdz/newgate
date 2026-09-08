package forward

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCountTokensAnsweredLocally Claude Code 周期性调 count_tokens 算上下文
// 水位，但 OpenAI 方言上游（DeepSeek 等）没有这个端点——转发只会沿链 404
// 一路到底，客户端界面刷一串报错。必须本地应答，且拦在 model 检查**之前**：
// 抓包见到过不带 model 字段的 count_tokens 请求。
func TestCountTokensAnsweredLocally(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"no such path"}`))
	}))
	defer up.Close()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// 故意不带 model 字段——这正是日志里 51 次
	// 「请求体顶层没有 model 字符串字段」的形态之一
	body := `{"messages":[{"role":"user","content":"看下这个，顺便数数 token"}],"tools":[]}`
	for _, path := range []string{
		"/v1/messages/count_tokens",
		"/a/claude/p/ds/v1/messages/count_tokens",
	} {
		resp, err := http.Post(front.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: 应本地 200，实际 %d: %s", path, resp.StatusCode, b)
		}
		if upstreamHit {
			t.Fatalf("%s: count_tokens 被转发到了上游", path)
		}
		var got struct {
			InputTokens int `json:"input_tokens"`
		}
		if err := json.Unmarshal(b, &got); err != nil || got.InputTokens <= 0 {
			t.Fatalf("%s: 应答不是 {input_tokens:N>0}: %s", path, b)
		}
	}
}
