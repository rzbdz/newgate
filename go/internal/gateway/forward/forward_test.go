package forward

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/gateway/health"
)

// 一段有代表性的 SSE：含 tool_call 分片、Unicode、空 data、大整数。
var sseChunks = []string{
	`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"background_cancel","arguments":""}}]}}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"id\":\"中文\"}"}}]}}]}` + "\n\n",
	`data: {"usage":{"total_tokens":9007199254740993}}` + "\n\n",
	"data: [DONE]\n\n",
}

// fakeUpstream 逐块吐 SSE，块之间有间隔——用来验证代理没有缓冲。
func fakeUpstream(t *testing.T, gap time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := ioutil.ReadAll(r.Body)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			w.WriteHeader(400)
			return
		}
		// 断言收到的是真实模型名，不是档位名
		var got string
		_ = json.Unmarshal(m["model"], &got)
		if got != "real-model-1" {
			t.Errorf("上游收到的 model = %q，应为 real-model-1", got)
		}
		if r.Header.Get("Authorization") != "Bearer sk-real" {
			t.Errorf("上游收到的 Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, c := range sseChunks {
			_, _ = io.WriteString(w, c)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(gap)
		}
	}))
}

// TestStreamByteFaithful 断言：响应体逐字节等于上游发出的内容。
func TestStreamByteFaithful(t *testing.T) {
	up := fakeUpstream(t, 0)
	defer up.Close()

	body, arrivals := streamThroughProxy(t, up.URL, 0)
	want := strings.Join(sseChunks, "")
	if body != want {
		t.Errorf("响应体被改动了\n want: %q\n got:  %q", want, body)
	}
	if len(arrivals) == 0 {
		t.Fatal("一个块都没收到")
	}
}

// TestStreamNotBuffered 断言：块是陆续到的，不是最后一次性倒出来。
// 这是流式体验的命门——缓冲了内容测试照样过，但用户看起来像���死。
func TestStreamNotBuffered(t *testing.T) {
	gap := 60 * time.Millisecond
	up := fakeUpstream(t, gap)
	defer up.Close()

	_, arrivals := streamThroughProxy(t, up.URL, gap)
	if len(arrivals) < 2 {
		t.Fatalf("只收到 %d 次数据，无法判断是否缓冲", len(arrivals))
	}
	first, last := arrivals[0], arrivals[len(arrivals)-1]
	spread := last - first
	// 上游总共铺开了 len(sseChunks)*gap，客户端观测到的时间跨度
	// 应该同量级；若被缓冲，spread 会接近 0
	if spread < gap {
		t.Errorf("看起来被缓冲了：首末块间隔仅 %v（上游间隔 %v，共 %d 块）",
			spread, gap, len(sseChunks))
	}
	t.Logf("首块 %v 后到达，首末跨度 %v，共 %d 次读取 —— 未缓冲",
		first, spread, len(arrivals))
}

// streamThroughProxy 起一个真实的代理实例，发一个流式请求，
// 返回完整响应体和每次读到数据的相对时刻。
func streamThroughProxy(t *testing.T, upstreamURL string, _ time.Duration) (string, []time.Duration) {
	t.Helper()

	srv := &Server{Port: 0}
	handler := http.HandlerFunc(srv.handleProxy)

	// 用一个只认 real-model-1 的解析器替代真实配置：
	// 直接构造 route，绕过 config 读盘
	testChain = func(role string) []resolve.Step {
		if role != "heavy" {
			return nil
		}
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(upstreamURL),
		}}
	}
	defer func() { testChain = nil }()

	front := httptest.NewServer(handler)
	defer front.Close()

	reqBody := `{"model":"newgate/heavy","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(front.URL+"/v1/chat/completions",
		"application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(resp.Body)
		t.Fatalf("状态 %d: %s", resp.StatusCode, b)
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Errorf("Transfer-Encoding 不该被透传给客户端，得到 %q", te)
	}

	start := time.Now()
	var out bytes.Buffer
	var arrivals []time.Duration
	br := bufio.NewReader(resp.Body)
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			arrivals = append(arrivals, time.Since(start))
		}
		if err != nil {
			break
		}
	}
	return out.String(), arrivals
}

func testProvider(baseURL string) domain.Provider {
	return domain.Provider{BaseURL: baseURL + "/v1", APIKey: "sk-real", Protocol: "openai"}
}

// TestClientCancelAbortsUpstream 断言：客户端中途断开时，上游请求也被取消。
// 没这个的话，用户在 opencode 里按 ESC，上游还会把整个响应跑完并照样计费。
func TestClientCancelAbortsUpstream(t *testing.T) {
	upstreamDone := make(chan error, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 50; i++ {
			if _, err := io.WriteString(w, fmt.Sprintf("data: chunk-%d\n\n", i)); err != nil {
				upstreamDone <- err
				return
			}
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				upstreamDone <- r.Context().Err() // 上游观测到取消
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		upstreamDone <- nil // 跑完了 = 取消没传播过去
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{
			Profile:  "test",
			Binding:  domain.Binding{Provider: "test-prov", Model: "real-model-1"},
			Provider: testProvider(up.URL),
		}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"newgate/heavy","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	// 读到前几块就断开
	buf := make([]byte, 256)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("首块都没读到: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case err := <-upstreamDone:
		if err == nil {
			t.Error("上游把 50 块全跑完了——取消没有传播过去，这会白烧 token")
		} else {
			t.Logf("上游观测到取消: %v —— 传播正确", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("3 秒内上游既没结束也没观测到取消")
	}
}

// TestNonStreamResponseByteFaithful 非流式响应也必须逐字节原样返回，
// 且不能漏掉/篡改上游的头。
func TestNonStreamResponseByteFaithful(t *testing.T) {
	// 故意用奇怪的 key 顺序、大整数、Unicode、非紧凑空白
	upBody := "{\n  \"z\": 1e10,\n  \"a\": 9007199254740993,\n" +
		"  \"txt\": \"中文 \\\"引号\\\" 和 \\\\ 反斜杠\",\n  \"nil\": null\n}"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Trace", "abc-123")
		w.Header().Set("Eo-Cache-Status", "MISS")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, upBody)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)

	if string(got) != upBody {
		t.Errorf("响应体被改动了\n want: %q\n got:  %q", upBody, got)
	}
	// 上游的业务头必须透传
	if resp.Header.Get("X-Upstream-Trace") != "abc-123" {
		t.Error("上游自定义头丢了")
	}
	if resp.Header.Get("Eo-Cache-Status") != "MISS" {
		t.Error("上游缓存状态头丢了")
	}
	// 我们自己的诊断头应该在
	if resp.Header.Get("X-Newgate-Route") == "" {
		t.Error("缺 X-Newgate-Route")
	}
}

// TestErrorResponseByteFaithful 上游报错时，错误原文必须一字不改地送到客户端
// ——吞掉上游错误信息是排查噩梦的主要来源。
func TestErrorResponseByteFaithful(t *testing.T) {
	errBody := `{"error":{"message":"Invalid schema for function 'background_cancel': null is not of type \"array\"","type":"invalid_request_error"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, errBody)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "p", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"newgate/heavy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != 400 {
		t.Errorf("状态码应透传 400，得到 %d", resp.StatusCode)
	}
	if string(got) != errBody {
		t.Errorf("上游错误原文被改动了\n want: %q\n got:  %q", errBody, got)
	}
	if resp.Header.Get("X-Newgate-Evidence") == "" {
		t.Error("4xx 应该带 X-Newgate-Evidence 指向证据文件")
	}
}

// TestClientCancelDuringConnectDoesNotBurnTheChain 断言：客户端在**连接阶段**
// 就取消时，不沿链重试、不记失败、不开熔断。
//
// 现场是这么坏的（10.0.50.11 的日志）：一次取消被当成 smt-claude 连接失败，
// 沿链换 smt-deepseek——可 context 已经死了，于是每个候选都瞬间失败，一次
// ESC 把整条链上三个 provider 的熔断器全打开。之后真正的请求没候选可用，
// 回 502，用户根本查不到源头。
func TestClientCancelDuringConnectDoesNotBurnTheChain(t *testing.T) {
	hit := make(chan string, 8)
	// 上游装死：收到请求就挂住。上限兜底，免得断言失败时整个测试卡死。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- r.URL.Path
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer up.Close()

	provs := []string{"prov-a", "prov-b", "prov-c"}
	testChain = func(role string) []resolve.Step {
		var out []resolve.Step
		for _, p := range provs {
			out = append(out, resolve.Step{
				Profile:  "test",
				Binding:  domain.Binding{Provider: p, Model: "real-model-1"},
				Provider: testProvider(up.URL),
			})
		}
		return out
	}
	defer func() { testChain = nil }()

	for _, p := range provs {
		health.Default.RecordSuccess(p) // 从干净状态开始
	}

	srv := &Server{Port: 0}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", front.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"newgate/heavy","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
		}
		close(done)
	}()

	select {
	case <-hit: // 第一个候选已经打上去了
	case <-time.After(3 * time.Second):
		t.Fatal("上游没收到请求")
	}
	cancel()
	<-done
	time.Sleep(150 * time.Millisecond) // 等 handler 收尾

	// 链上后面的候选一个都不该被打
	select {
	case p := <-hit:
		t.Fatalf("客户端都取消了还沿链重试了下一个候选（%s）", p)
	default:
	}
	for _, p := range provs {
		if !health.Default.Available(p) {
			t.Errorf("provider %s 被一次客户端取消打开了熔断器", p)
		}
	}
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("客户端取消被记成了 %d 次上游失败", n)
	}
}
