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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"

	"github.com/rzbdz/newgate/go/modules/breaker"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/gateway/metrics"
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

	srv := newTestServer()
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

	srv := newTestServer()
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

	srv := newTestServer()
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

	srv := newTestServer()
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

// shapeTestDetector 是 forward 这一层的测试判据：认「点名了 reasoning_content
// 又要求逐字回传」的 400。
//
// 为什么在 forward 的测试里自己写一条，而不是 import modules/deepseek 用它那条：
// 转发路径**不认识任何上游**（这正是 2026-09-17 那次重构成立的理由——判据由
// 上游模块注册，core 只读 Result.Shape 那个名字）。在单测里 import deepseek
// 等于把刚拆掉的那条耦合从测试里接回去：真判据改了文案，这里会跟着红，而它
// 红的原因跟 forward 的行为毫无关系。
//
// 分工是「测试跟着谁知道这件事走」：真判据的真值表在 modules/deepseek/shape_test.go，
// 判据从模块 Start 一路接到转发路径的**接线**在 testing/system（真组件图 + 假
// 上游）；这里只锁 forward 与健康表的契约——Shape 非空时它该做什么。
type shapeTestDetector struct{}

func (shapeTestDetector) Name() string { return "test-shape" }

func (shapeTestDetector) Match(status int, body []byte) bool {
	if status != 400 {
		return false
	}
	return bytes.Contains(body, []byte("reasoning_content")) &&
		bytes.Contains(body, []byte("must be passed back"))
}

var _ breaker.ShapeDetector = shapeTestDetector{}

// TestShapeErrorIsCountedThenLoggedWithEvidence 锁住转发路径对「形状错误」的
// 全部义务。健康表说是形状错误（Result.Shape 非空）之后，forward 要：
//
//  1. 原样透传上游原文（不静默，客户端看到的就是上游说的）；
//  2. 不把它记成可用性失败——binding 必须还在链上；
//  3. 日志里打出**是哪条判据**认的，外加一行「现场已存档」；
//  4. 现场落进 dump/shape-400-<判据>/（专用目录，不参与 req-*/err-* 的滚动清理）。
//
// 现场动机（2026-09-17，health.json + 日志）：deepseek-flash 的这条 400 被当成
// 可用性失败记进熔断器，连续两发就把一条本来能用的 binding 摘掉；而 probe 一直
// 绿——探活发的是最小请求，触发不到「思考模式要求逐字回传」。摘要写得明确：
// 「同一份 body 换哪个 provider 都一样错」的问题不该记在任何一家的账上。
func TestShapeErrorIsCountedThenLoggedWithEvidence(t *testing.T) {
	sandboxState(t, `{"port": 0}`)
	metrics.Default.Reset()
	t.Cleanup(func() { metrics.Default.Reset(); testChain = nil })

	reasoningErr := `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API."}`
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, reasoningErr)
	}))
	defer up.Close()

	// 单站链（IsLast）：形状错误会继续沿链试，只有链尾那一发才走「定案」分支，
	// 也就是打专属日志、存实地证据的那条路径。多站链测不到它。
	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "ds",
			Binding:  domain.Binding{Provider: "ds-shape", Model: "deepseek-flash"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv, logBuf := newLoggingTestServer()
	if _, err := srv.Health.RegisterShapeDetector(shapeTestDetector{}); err != nil {
		t.Fatalf("注册判据: %v", err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// 连发 3 次——可用性账本的阈值是 2，没有判据时第二发就该摘牌了。
	for i := 0; i < 3; i++ {
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"newgate/heavy"}`))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("第 %d 发状态码 = %d, want 400（应原样透传，不是别的错误）", i+1, resp.StatusCode)
		}
		if string(got) != reasoningErr {
			t.Fatalf("第 %d 发上游原文被改动了\n want: %q\n got:  %q", i+1, reasoningErr, got)
		}
	}
	if hits != 3 {
		t.Fatalf("上游被打了 %d 次, want 3", hits)
	}
	if !srv.Health.Available("ds-shape", "deepseek-flash") {
		t.Error("形状 400 把熔断器打开了——这类错误不该记进可用性账本")
	}
	for _, s := range srv.Health.Snapshot() {
		if s.Provider != "ds-shape" {
			continue
		}
		if s.ShapeSkips != 3 || s.Fails != 0 || s.Open {
			t.Errorf("形状错误应当只计数: %+v", s)
		}
	}

	// 判据名必须进日志：形状判据是多家上游各自的，只说「命中了形状错误」在
	// 有两家同时报 400 时毫无用处。转发路径不认识那些字符串，名字是它唯一的线索。
	logs := logBuf.String()
	if !strings.Contains(logs, "[shape-400]") || !strings.Contains(logs, "判据 test-shape") {
		t.Errorf("日志没有说清是被哪条判据认下的:\n%s", logs)
	}
	if !strings.Contains(logs, "现场已存档") {
		t.Errorf("证据没落盘（这类 400 偶发又致命，丢了就复现不了）:\n%s", logs)
	}

	// 证据落进专用目录，名字取自判据 → 加一条新判据自动多一个目录，转发路径不改。
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "dump", "shape-400-test-shape")
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("读证据目录 %s: %v（形状 400 的现场必须单独存档）", dir, err)
	}
	if len(ents) == 0 {
		t.Fatalf("证据目录 %s 是空的", dir)
	}
	for _, want := range []string{"client-sent.json", "we-sent.json", "upstream-said.json", "audit.txt"} {
		if _, err := os.Stat(filepath.Join(dir, ents[0].Name(), want)); err != nil {
			t.Errorf("证据目录里缺 %s: %v", want, err)
		}
	}
}

// TestShapeDirNameIsPathSafe：判据名会进文件路径，而判据是**别的模块**注册进来
// 的代码——不设防就等于让一个注册项决定往哪写文件。
func TestShapeDirNameIsPathSafe(t *testing.T) {
	tests := []struct{ in, want string }{
		{"deepseek", "shape-400-deepseek"},
		{"a.b_c-1", "shape-400-a.b_c-1"},
		{"../escape", "shape-400-.._escape"},
		{"a/b", "shape-400-a_b"},
		{"", "shape-400-unknown"},
		{"中文", "shape-400-__"},
	}
	for _, tt := range tests {
		if got := shapeDirName(tt.in); got != tt.want {
			t.Errorf("shapeDirName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// 结果里绝不能出现分隔符：路径拼接的反面教材是「上游说了算的字符串」。
	for _, name := range []string{"../x", "a/b", "a\\b"} {
		if got := shapeDirName(name); strings.ContainsAny(got, `/\`) {
			t.Errorf("shapeDirName(%q) = %q 仍然带路径分隔符", name, got)
		}
	}
}

// TestOtherClientErrorDoesNotBlameTheProvider 是上一条的对照组，锁住这条政策
// 的另一侧：**任何** 400 都不记在这家上游头上，不管我们的形状检测器认不认得它。
//
// 这条测试 2026-09-17 的方向是反的（当时断言「非形状 400 连发两次必须开闸」，
// 理由是「schema 真坏的 provider 否则永远摘不掉」）。翻转它的理由：
//
//   - 400 的含义就是「你这份请求不对」，而形状检测器只是几条字符串匹配，认不
//     出来不等于问题在上游——按认不出来的 400 摘牌，等于让一个补丁的盲区决定
//     摘谁，正好是 2026-09-17 那次 reasoning 回传 400 事故的形状；
//   - 现在摘牌会自己回来（半开 + 退避），但代价不对称：误摘一次要等 60s 起
//     步、最多 10 分钟才回到链上，期间用户的请求被悄悄换给了别的模型；
//   - 「这家上游根本不通」不是被动路径能下的结论，`newgate probe` 才是权威
//     手段：探活发的是最小合法请求，base 错/版本错的 provider 会当场被 probe
//     摘掉，而它永远不会因为用户某一轮的对话形状被误判。
//
// 结论：被动路径只认「明确是上游的错」的收场，4xx 的歧义交给 fallback_on_400
// 去表达（要不要换个上游试试），而不是交给记账。
func TestOtherClientErrorDoesNotBlameTheProvider(t *testing.T) {
	schemaErr := `{"error":{"message":"Invalid schema: missing required field 'name'"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, schemaErr)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "schema-bad", Model: "real-model-1"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"newgate/heavy"}`))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || string(got) != schemaErr {
			t.Fatalf("第 %d 发应原样透传 400 与上游原文，得到 %d %q", i+1, resp.StatusCode, got)
		}
	}
	if !srv.Health.Available("schema-bad", "real-model-1") {
		t.Error("非形状 400 把这条 binding 摘掉了——400 不该记在上游头上")
	}
	for _, s := range srv.Health.Snapshot() {
		if s.Provider == "schema-bad" && (s.Fails > 0 || s.ShapeSkips > 0 || s.Open) {
			t.Errorf("非形状 400 进账本了: %+v", s)
		}
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

	srv := newTestServer()
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
		if !srv.Health.Available(p, "real-model-1") {
			t.Errorf("provider %s 被一次客户端取消打开了熔断器", p)
		}
	}
	if n := atomic.LoadUint64(&srv.failures); n != 0 {
		t.Errorf("客户端取消被记成了 %d 次上游失败", n)
	}
}
