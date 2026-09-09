package forward

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
)

// TestNonStreamFirstByteTimeoutFailsOver 非流式请求的首字节等待有独立上限
// （2026-09-09 实抓：relay 一发 air 请求 89s 无响应头，Claude Code 的权限
// 分类器在等，用户整个终端陪绑）。超时按连接失败处理：沿链换下一个候选，
// 绝不无限等。流式不收这条约束（长思考响应合法）。
func TestNonStreamFirstByteTimeoutFailsOver(t *testing.T) {
	saved := firstByteTimeoutNonStream
	firstByteTimeoutNonStream = 400 * time.Millisecond
	defer func() {
		firstByteTimeoutNonStream = saved
		testChain = nil
	}()

	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // 装死：远超测试用的 400ms 上限
		_, _ = w.Write([]byte(`{"from":"hung"}`))
	}))
	defer hung.Close()
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"from":"alive"}`))
	}))
	defer alive.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "a", Binding: domain.Binding{Provider: "hung-prov", Model: "m1"}, Provider: testProvider(hung.URL)},
			{Profile: "b", Binding: domain.Binding{Provider: "alive-prov", Model: "m2"}, Provider: testProvider(alive.URL)},
		}
	}

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	start := time.Now()
	resp, err := http.Post(front.URL+"/v1/chat/completions",
		"application/json", strings.NewReader(`{"model":"mid","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != 200 || !strings.Contains(string(b), "alive") {
		t.Fatalf("应沿链换到 alive 并 200，实际 %d: %s", resp.StatusCode, b)
	}
	if resp.Header.Get("X-Newgate-Failover") == "" {
		t.Fatal("转移了必须带 X-Newgate-Failover（不静默）")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("等了 %v 才转移——没等到装死服务的 3s，说明超时没起作用", elapsed)
	}
}
