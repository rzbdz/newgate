package forward

import (
	"context"
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

// classifierBody 复刻 Bash 权限分类器的形态（实抓：非流式 + system
// 自报 "You are a security monitor"）。
const classifierBody = `{"model":"mid","max_tokens":2112,` +
	`"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}],` +
	`"messages":[{"role":"user","content":"classify"}]}`

// compactBody 复刻 /compact 总结请求的形态（非流式，**没有**分类器
// marker——大输入、不挡交互，不该吃紧超时）。
const compactBody = `{"model":"heavy","max_tokens":10240,` +
	`"system":[{"type":"text","text":"You are a conversation summarizer. Summarize this conversation."}],` +
	`"messages":[{"role":"user","content":"…transcript…"}]}`

// TestClassifierFirstByteTimeoutFailsOver 紧首字节超时**只给分类器**：
// 超时按连接失败处理，沿链换下一个候选，绝不无限等。现场动机
// （2026-09-09 实抓）：分类器挡在交互通路上，挂住它 = 冻住整个会话。
func TestClassifierFirstByteTimeoutFailsOver(t *testing.T) {
	// 400ms 的分类器首字节上限——直接写进沙箱 state.json
	sandboxState(t, `{"port": 0, "timeouts": {"classifier_first_byte_ms": 400}}`)
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
	resp, err := http.Post(front.URL+"/a/claude/v1/messages",
		"application/json", strings.NewReader(classifierBody))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != 200 || !strings.Contains(string(b), "alive") {
		t.Fatalf("分类器应沿链换到 alive 并 200，实际 %d: %s", resp.StatusCode, b)
	}
	if resp.Header.Get("X-Newgate-Failover") == "" {
		t.Fatal("转移了必须带 X-Newgate-Failover（不静默）")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("等了 %v 才转移——没等到装死服务的 3s，说明紧超时没起作用", elapsed)
	}
	if got := metrics.Default.Snapshot()["timeout.first_byte.non_stream"]; got != 1 {
		t.Errorf("timeout.first_byte.non_stream = %d，应为 1", got)
	}
	if got := metrics.Default.Snapshot()["chain.failover"]; got != 1 {
		t.Errorf("chain.failover = %d，应为 1", got)
	}
}

// TestCompactNotTightTimeout 非 / 分类器形态的非流式请求**不吃**紧超时
// （2026-09-09 实抓教训：compact 500KB+ 大输入被 12s 一刀切在链上
// 三连掐死）。装死上游 + 紧超时配置在场，compact 请求也该等到标准
// 150s——测试里只验证它**不**在 400ms 处被杀（等满 1.2s 后客户端
// 主动取消，上游还没被超时掉）。
func TestCompactNotTightTimeout(t *testing.T) {
	sandboxState(t, `{"port": 0, "timeouts": {"classifier_first_byte_ms": 400, "first_byte_ms": 150000}}`)
	metrics.Default.Reset()
	t.Cleanup(func() { metrics.Default.Reset(); testChain = nil })

	var upstreamGot bool
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamGot = true
		time.Sleep(3 * time.Second) // 慢，但没超过 150s 标准上限
		_, _ = w.Write([]byte(`{"from":"hung-slow"}`))
	}))
	defer hung.Close()
	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "a", Binding: domain.Binding{Provider: "hung-prov", Model: "m1"}, Provider: testProvider(hung.URL)},
		}
	}

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// 客户端 1.2s 后放弃（远晚于 400ms 的紧超时，远早于 150s 标准上限）
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		front.URL+"/a/claude/v1/messages", strings.NewReader(compactBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}

	if got := metrics.Default.Snapshot()["timeout.first_byte.non_stream"]; got != 0 {
		t.Fatalf("compact 不该吃分类器紧超时，却超时了（counter=%d）", got)
	}
	if !upstreamGot {
		t.Fatal("请求没到上游")
	}
	// 客户端断开后 proxy goroutine 处理取消需要一点时间——轮询等它记账
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.Default.Snapshot()["client.cancel"] == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("应该是客户端取消（counter=%d）", metrics.Default.Snapshot()["client.cancel"])
}
