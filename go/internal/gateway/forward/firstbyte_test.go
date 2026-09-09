package forward

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/gateway/metrics"
)

// sandboxState 铺一个最小 NEWGATE_HOME（state.json + 空 providers），
// 让超时等配置从测试自己的值来，不碰真实 ~/.config/newgate。
func sandboxState(t *testing.T, stateJSON string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stateJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestNonStreamFirstByteTimeoutFailsOver 非流式请求的首字节等待上限从
// state.json 的 timeouts 来（热加载，改配置不用重编译）：超时按连接失败
// 处理，沿链换下一个候选，绝不无限等。
// 现场动机（2026-09-09 实抓）：relay 一发 air 请求 89s 无响应头，Claude
// Code 的权限分类器在等，用户整个终端陪绑。流式不收这条约束（长思考
// 响应合法）。
func TestNonStreamFirstByteTimeoutFailsOver(t *testing.T) {
	// 400ms 的非流式首字节上限——直接写进沙箱 state.json
	sandboxState(t, `{"port": 0, "timeouts": {"first_byte_non_stream_ms": 400}}`)
	metrics.Default.Reset()
	t.Cleanup(func() { metrics.Default.Reset(); testChain = nil })

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
	// 计数器也要对得上：非流式首字节超时 + 换链成功各一笔
	if got := metrics.Default.Snapshot()["timeout.first_byte.non_stream"]; got != 1 {
		t.Errorf("timeout.first_byte.non_stream = %d，应为 1", got)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 1 {
		t.Errorf("chain.failover = %d，应为 1", got)
	}
}
