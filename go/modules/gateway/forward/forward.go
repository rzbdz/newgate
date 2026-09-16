package forward

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/lib/logx"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/dialect"
	"github.com/rzbdz/newgate/go/modules/gateway/health"
	"github.com/rzbdz/newgate/go/modules/gateway/metrics"
	"github.com/rzbdz/newgate/go/modules/gateway/probe"
	"github.com/rzbdz/newgate/go/modules/gateway/protocol"
	"github.com/rzbdz/newgate/go/modules/gateway/quirk"
	"github.com/rzbdz/newgate/go/modules/gateway/rewrite"
	schema "github.com/rzbdz/newgate/go/modules/gateway/rewrite/schema"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
	"github.com/rzbdz/newgate/go/modules/gateway/thinkcache"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// Server 拥有代理数据面的 listener、热配置快照和优雅交接状态。
// 它是运行资源而不是组件端口；生命周期由上层 daemon/CLI 明确控制。
type Server struct {
	Port   int
	Logger *log.Logger
	// Watch 配置快照的持有者。热路径只做一次原子指针读，不碰文件系统。
	// 改了配置**不需要重启**：watcher 换页后，新进来的请求就用新配置，
	// 正在跑的会话不受影响（各 agent 读各自的绑定）。
	Watch *store.Watcher

	srv      *http.Server
	ln       net.Listener // 优雅交接要把它作为 fd 移交新进程
	requests uint64
	failures uint64
	started  time.Time

	stopOnce sync.Once
	stopCh   chan struct{}

	// 优雅交接的排空状态（交接后的**旧**进程用）：
	//   draining=1 之后，本进程的 pid/lock 已改写到新进程名下——任何
	//   退出路径都不许再清它们；主循环（Serve 返回处）要等在途请求
	//   流完才能退，否则 Shutdown 一关 listener 主函数就 return，
	//   进程当场消失，排空等于没排。
	drainOnce sync.Once
	drainCh   chan struct{}
	drainFlag int32
}

// New 构造尚未监听的 Server，使配置和日志依赖在启动副作用前就完整可见。
func New(port int, lg *log.Logger, w *store.Watcher) *Server {
	return &Server{Port: port, Logger: lg, Watch: w,
		stopCh: make(chan struct{}), drainCh: make(chan struct{})}
}

// RequestStop 外部请求停机（控制端点用）。幂等，重复调用无害。
// stopCh 为 nil（直接拿 &Server{} 字面量构造的测试桩）时静默跳过。
func (s *Server) RequestStop() {
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
}

// StopRequested 收听停机请求。收到即代表有人（通常是别的用户的
// `newgate stop`）让这个进程退出。
func (s *Server) StopRequested() <-chan struct{} { return s.stopCh }

// Draining 本进程是否处于优雅交接的排空期（socket 已移交新进程）。
// 排空期的退出路径不能清 pid/lock——那是新进程的了。
func (s *Server) Draining() bool { return atomic.LoadInt32(&s.drainFlag) == 1 }

// Drained 在途请求排空完成的信号。只在 Draining 时有意义。
func (s *Server) Drained() <-chan struct{} { return s.drainCh }

// snap 当前配置快照。watcher 缺失时退化为直接读盘（测试路径）。
func (s *Server) snap() *store.Snapshot {
	if s.Watch != nil {
		return s.Watch.Current()
	}
	sn, err := store.Load()
	if err != nil {
		return nil
	}
	return sn
}

func (s *Server) logf(format string, a ...interface{}) {
	if s.Logger != nil {
		s.Logger.Printf(format, a...)
	}
}

func (s *Server) Start() error {
	health.Default.SetErrorHandler(func(err error) {
		s.logf("[health] 全局健康表写入失败（继续使用内存状态）: %v", err)
	})
	if err := health.Default.UseFile(paths.HealthFile()); err != nil {
		s.logf("[health] 全局健康表加载失败（继续纯内存）: %v", err)
	}
	probe.LoadCachedCapabilities()
	mux := http.NewServeMux()
	mux.HandleFunc("/__newgate/status", s.handleStatus)
	mux.HandleFunc("/__newgate/metrics", s.handleMetrics)
	mux.HandleFunc("/__newgate/health", s.handleHealth)
	mux.HandleFunc("/__newgate/stop", s.handleControlStop)
	mux.HandleFunc("/__newgate/upgrade", s.handleControlUpgrade)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/", s.handleProxy)

	ln, err := s.listen()
	if err != nil {
		return err
	}
	s.started = time.Now()
	s.srv = &http.Server{Handler: mux}
	s.ln = ln
	s.signalReady() // 交接进来的进程：告诉父进程「socket 已接上」
	s.logf("[proxy] 监听 127.0.0.1:%d", s.Port)
	return s.srv.Serve(ln)
}

// listen 拿监听 socket：父进程交接来的 fd 优先（优雅升级路径），否则
// 自己 listen。继承时 fd 号从 NEWGATE_LISTENER_FD 读，读完立刻 unset——
// 环境变量不能传染给这个进程之后 spawn 的任何东西（比如它自己的升级
// 子进程、或懒启动路径的普通 Spawn），否则 fd3 会被当成 socket 用。
func (s *Server) listen() (net.Listener, error) {
	if fdStr := os.Getenv("NEWGATE_LISTENER_FD"); fdStr != "" {
		os.Unsetenv("NEWGATE_LISTENER_FD")
		fd, err := strconv.Atoi(fdStr)
		if err != nil {
			return nil, fmt.Errorf("NEWGATE_LISTENER_FD=%q 不是 fd 号: %w", fdStr, err)
		}
		f := os.NewFile(uintptr(fd), "inherited-listener")
		ln, err := net.FileListener(f)
		if err != nil {
			return nil, fmt.Errorf("继承监听 fd %d 失败: %w", fd, err)
		}
		if _, ok := ln.(*net.TCPListener); !ok {
			ln.Close()
			return nil, fmt.Errorf("继承的 fd %d 不是 TCP 监听器", fd)
		}
		// 端口号以真身为准：继承路径下 s.Port 参数可能只是父进程的复述
		if a, ok := ln.Addr().(*net.TCPAddr); ok {
			s.Port = a.Port
		}
		return ln, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port))
	if err != nil {
		return nil, fmt.Errorf("监听 127.0.0.1:%d 失败: %w", s.Port, err)
	}
	return ln, nil
}

// signalReady 优雅交接的另一半握手：进入 accept 循环前往父进程给的
// 管道写一个字节。没有交接（正常 Spawn 启动）时是空操作。
func (s *Server) signalReady() {
	fdStr := os.Getenv("NEWGATE_READY_FD")
	if fdStr == "" {
		return
	}
	os.Unsetenv("NEWGATE_READY_FD")
	if fd, err := strconv.Atoi(fdStr); err == nil {
		if f := os.NewFile(uintptr(fd), "ready-pipe"); f != nil {
			_, _ = f.Write([]byte{1})
			_ = f.Close()
		}
	}
}

func (s *Server) Shutdown() {
	health.Default.Flush()
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.snap()
	if snap == nil {
		s.fail(w, 400, "配置读不出 ← 跑 `newgate doctor`")
		return
	}
	st := snap.State
	tc := thinkcache.Default.Stats()
	writeJSON(w, 200, map[string]interface{}{
		"ok":              true,
		"default_profile": st.DefaultProfile,
		"active":          st.Active,
		"port":            s.Port,
		"requests":        atomic.LoadUint64(&s.requests),
		"failures":        atomic.LoadUint64(&s.failures),
		"breakers":        health.Default.Snapshot(),
		"uptime_s":        int(time.Since(s.started).Seconds()),
		"tiers":           domain.Roles,
		// 给 `newgate restart` 探测用：支持优雅交接（socket 移交）。
		// 旧版 daemon 没这个字段——restart 看到缺失就退回 stop+start，
		// 绝不盲发 /__newgate/upgrade（那会被 catch-all 转发到上游）。
		"handoff": true,
		// 推理内容缓存：只报计数，绝不报内容
		"thinkcache": map[string]interface{}{
			"entries":   tc.Entries,
			"bytes":     tc.Bytes,
			"max_bytes": tc.MaxBytes,
			"hits":      tc.Hits,
			"misses":    tc.Misses,
			"puts":      tc.Puts,
			"evictions": tc.Evictions,
		},
	})
}

type probeHealthObservation struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Context   int    `json:"context_bytes"`
	Error     string `json:"error,omitempty"`
}

// handleHealth 接收 probe 的主动健康结论，写入 daemon 唯一的全局熔断表。
// probe 是 4-token 极小请求；超过分类器首字节阈值仍未完成，就不具备进入
// 交互 fallback 链的资格，即使它最终回了 200。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": "只接受 POST"})
		return
	}
	snap := s.snap()
	if snap == nil || snap.State.ControlToken == "" ||
		r.Header.Get("Authorization") != "Bearer "+snap.State.ControlToken {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": "control token 不符"})
		return
	}
	var req struct {
		Observations []probeHealthObservation `json:"observations"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "请求 JSON 无效"})
		return
	}
	threshold := snap.State.Timeouts.ClassifierFirstByte()
	opened := 0
	for _, o := range req.Observations {
		if o.Provider == "" || o.Model == "" {
			continue
		}
		grade, didOpen := health.Default.RecordProbe(o.Provider, o.Model, o.Status, o.Context,
			time.Duration(o.LatencyMs)*time.Millisecond, threshold, o.Error)
		if didOpen {
			opened++
			s.logf("[probe] 熔断 %s/%s：%s（至少 60s，之后须 probe 成功才回链）",
				o.Provider, o.Model, grade)
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "opened": opened, "breakers": health.Default.Snapshot(),
	})
}

// handleMetrics 网关计数器（只读，只听 127.0.0.1）。`newgate metrics` 的
// 数据源；计数随 daemon 重启归零。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"uptime_s": int(time.Since(s.started).Seconds()),
		"metrics":  metrics.Default.Snapshot(),
	})
}

// handleControlStop 控制端点：给「读得到共享配置、却发不出信号」的用户
// 停机用（daemon.Stop 在 EPERM 时的兜底路径）。
//
// 鉴权：state.json 里的 ControlToken（Bearer）。端点只听 127.0.0.1，
// 且令牌与上游 key 同文件同级保密——能拿到它的进程早已越过了这层防护。
func (s *Server) handleControlStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": "只接受 POST"})
		return
	}
	tok := ""
	if snap := s.snap(); snap != nil {
		tok = snap.State.ControlToken
	}
	// 常数时间比较：不让本地进程靠响应耗时逐位猜令牌
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": "control token 不符"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "bye": true})
	s.logf("[proxy] 收到控制停机请求（来自 %s），退出", r.RemoteAddr)
	// 先让 200 刷出缓冲区，再触发退出
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.RequestStop()
	}()
}

// handleControlUpgrade 优雅升级（nginx upgrade 的 Go 版）：把监听 socket
// 移交给新二进制，新进程接上后旧进程排空在途请求再退。
//
// 为什么要有它：开发 newgate 的会话本身就穿行在代理里，stop/start 的
// 断流窗口会把正在用代理的 Claude Code 直接打断。交接路径下：
//   - 在途请求（含 SSE 长流）由本进程**流完为止**，不掐断；
//   - 新连接立刻由新进程 accept，客户端无感知；
//   - 不摘 shim、不动接管状态——只是换了个进程继续服务。
//
// 鉴权与 /__newgate/stop 同源（ControlToken）。
func (s *Server) handleControlUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": "只接受 POST"})
		return
	}
	tok := ""
	if snap := s.snap(); snap != nil {
		tok = snap.State.ControlToken
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": "control token 不符"})
		return
	}
	if s.ln == nil {
		writeJSON(w, 503, map[string]interface{}{"ok": false, "error": "监听句柄还没就绪"})
		return
	}
	info, err := daemon.SpawnHandoff(s.Port, s.ln)
	if err != nil {
		// 交棒失败：本进程继续独占 socket，服务没受任何影响
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	// pid/lock 立刻改到新进程名下——排空期间 status 也要指向未来
	if err := daemon.AdoptRuntime(info); err != nil {
		_ = syscall.Kill(info.PID, syscall.SIGKILL)
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	s.logf("[proxy] 优雅交接：socket 已移交新进程 pid %d，本进程开始排空在途请求", info.PID)
	writeJSON(w, 200, map[string]interface{}{"ok": true, "new_pid": info.PID})
	// 排空：等在途请求（含 SSE 流）自然结束，上限 10 分钟，然后退。
	// 两个关键点：
	//   - 用 Shutdown（等在途），绝不用 srv.Close()（硬掐）；
	//   - 主循环会在 Serve 返回处等 Drained()——否则 listener 一关主函数
	//     就 return，进程当场消失，这里等再久也白等。
	// 退出一律 os.Exit：跳过 Serve 里 defer 的 RemoveLock（pid/lock 已是
	// 新进程的，不能删）。
	go func() {
		atomic.StoreInt32(&s.drainFlag, 1)
		time.Sleep(100 * time.Millisecond) // 让 200 先刷出去
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if s.srv != nil {
			_ = s.srv.Shutdown(ctx)
		}
		s.drainOnce.Do(func() { close(s.drainCh) })
		s.logf("[proxy] 排空完成，退出（socket 由 pid %d 继续）", info.PID)
		os.Exit(0)
	}()
}

// handleModels 让 opencode 能发现我们暴露的语义档位。
//
// 模块贡献的动态角色键（omo-sisyphus / cat-deep）也列出来：它们同样是客户端
// 可以点名的 model id，藏着不列只会让 opencode 报「模型不存在」。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	var data []map[string]interface{}
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return // 内置别名（normal→mid）也在角色表里，会与档位重名
		}
		seen[id] = true
		data = append(data, map[string]interface{}{
			"id": id, "object": "model", "owned_by": "newgate",
		})
	}
	for _, tier := range domain.Roles {
		add(tier)
	}
	for _, extra := range domain.ExtraRoles() {
		add(extra.Key)
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

// Target 从 URL 路径里解析出的路由意图。
//
// 路径文法：
//
//	/a/<agent>[/p/<profile>]/v1/...   带 agent 身份
//	/v1/...                           兼容：用全局默认 profile
//
// 为什么用路径而不是 header：claude / opencode 都不允许我们往它们的请求里
// 塞自定义 header，但 baseURL 是 bootstrap 时我们自己写进去的。路径方案
// 完全无状态——不需要注册会话、不需要令牌生命周期，代理重启也不受影响。
type Target struct {
	TaskCreate string // 空 = 未指定
	Profile    string // 非空 = 本次调用显式覆盖，不改全局状态
	Suffix     string // 转发给上游的路径后缀，形如 /chat/completions
}

func parseTarget(p string) Target {
	var t Target
	for {
		switch {
		case strings.HasPrefix(p, "/a/"):
			rest := p[3:]
			if i := strings.IndexByte(rest, '/'); i < 0 {
				t.TaskCreate, p = rest, ""
			} else {
				t.TaskCreate, p = rest[:i], rest[i:]
			}
		case strings.HasPrefix(p, "/p/"):
			rest := p[3:]
			if i := strings.IndexByte(rest, '/'); i < 0 {
				t.Profile, p = rest, ""
			} else {
				t.Profile, p = rest[:i], rest[i:]
			}
		default:
			t.Suffix = strings.TrimPrefix(p, "/v1")
			return t
		}
	}
}

// testChain 仅供测试注入，绕过磁盘配置与真实上游。生产路径永远是 nil。
// 有它才能保证单测不出网——docs/10-testing-security.md「测试不出网，出网即失败」。
var testChain func(tier string) []resolve.Step

// handleCountTokens 本地应答 count_tokens（粗估兜底）。
//
// Claude Code 周期性地问 token 数（上下文水位条、自动压缩阈值都靠它），
// 但不是所有上游都有这个端点——聚合器实测（2026-09，api.rvcompute.com）
// /messages 200 而 /messages/count_tokens 404。所以先试转发拿真值
// （forwardCountTokens，按 provider 学），接不住再走到这里：按请求体
// 字节数 / 4 粗估（英文 ≈ 4 字符/token；中文 UTF-8 ≈ 3 字节/字、
// 1 字/token，估出来偏大——对水位条来说宁可早压缩，无害）。
//
// 逐轮的真实计数走 /messages 响应里的 usage，不经过这里。
func (s *Server) handleCountTokens(w http.ResponseWriter, reqID uint64, body []byte) {
	n := len(body) / 4
	s.logf("[proxy] #%d count_tokens（%d 字节）→ 本地粗估 %d tokens", reqID, len(body), n)
	writeJSON(w, 200, map[string]interface{}{"input_tokens": n})
}

// forwardCountTokens 把 count_tokens 转发给上游拿真值。接住了返回 true。
//
// 为什么值得：本地只有粗估，上游的 tokenizer 才是真值——水位条和自动
// 压缩阈值都靠它。但它是 anthropic 方言的私有端点，openai 方言上游和
// 很多聚合器没有，所以按 (provider, model) 学，gate 层面 lazy probe：
//
//	没探过 → 试发一发（这本身就是 probe）；404/405 = 明确没有，记下，
//	          此后退回本地粗估不再白跑；2xx = 有，记下，此后一直拿真值
//	连接失败 / 429 / 401 → 不学（「现在不行」≠「没有」），本次退回本地
//
// 模型注入：count_tokens 请求不带 model 字段，补**主力档**链头——
// Claude Code 的主循环跑在 opus 槽（四档化后 opus 槽 = normal 档，
// 2026-09-16；之前是 heavy），数出来的才是将要处理这段对话的 tokenizer。不走链、
// 不碰熔断器：数 token 失败不算上游病。
func (s *Server) forwardCountTokens(w http.ResponseWriter, r *http.Request,
	body []byte, tgt Target, reqID uint64) bool {

	head, ok := s.mainLoopHead(tgt)
	if !ok {
		return false
	}
	if ctOK, known := dialect.Supports(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens); known && !ctOK {
		return false // 探过了：这个上游没有 count_tokens
	}

	// 补 model 字段（请求通常不带）；带了就尊重客户端的
	var out []byte
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" {
		out = body
	} else {
		nb, err := rewrite.InsertTopLevelRaw(body, "model", []byte(strconv.Quote(head.Binding.Model)))
		if err != nil {
			return false
		}
		out = nb
	}

	target := head.Provider.URL("/messages/count_tokens")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(out))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req.Header, head.Provider)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			s.logf("[proxy] #%d count_tokens 转发失败（%v），本次退回本地粗估", reqID, err)
		}
		return false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 405:
		if dialect.MarkUnsupported(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens) {
			s.logf("[proxy] #%d 学到：%s 没有 count_tokens 端点（上游 %d）——退回本地粗估，不再试",
				reqID, head.Binding, resp.StatusCode)
		}
		metrics.Default.Inc("count_tokens.probe_404")
		return false
	case resp.StatusCode >= 400:
		s.logf("[proxy] #%d count_tokens 上游 %d，退回本地粗估", reqID, resp.StatusCode)
		return false
	}
	dialect.Mark(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens)
	metrics.Default.Inc("count_tokens.forwarded")

	rb, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Newgate-Route", "count_tokens -> "+head.Binding.String())
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(rb)
	s.logf("[proxy] #%d count_tokens → %s 上游真值: %s", reqID, head.Binding, trim(string(rb)))
	return true
}

// mainLoopHead 主循环档（normal）的链头（含完整 provider 记录）。
// count_tokens 不带 model，转发时按它补。用 PrimaryBinding（忽略熔断器）：
// 数 token 用配置里排第一的就行，不值得为它触发 fallback 语义。
func (s *Server) mainLoopHead(tgt Target) (resolve.Step, bool) {
	if testChain != nil {
		if steps := testChain("normal"); len(steps) > 0 {
			return steps[0], true
		}
		return resolve.Step{}, false
	}
	snap := s.snap()
	if snap == nil {
		return resolve.Step{}, false
	}
	active := tgt.Profile
	if active == "" {
		active = snap.State.ActiveFor(tgt.TaskCreate)
	}
	b, ok := resolve.PrimaryBinding("normal", snap.Profiles, snap.Providers, active)
	if !ok {
		return resolve.Step{}, false
	}
	p, ok := snap.Providers.Providers[b.Provider]
	if !ok {
		return resolve.Step{}, false
	}
	return resolve.Step{Profile: active, Binding: b, Provider: p}, true
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	atomic.AddUint64(&s.requests, 1)
	metrics.Default.Inc("requests.total")

	// /api/hello：Claude Code 的连通性探针（HEAD/GET，无 body）。探的是
	// 「API 基地址活着吗」——我们就是它的 API，本地应 200；掉进下面的
	// model 解析只会刷一串 400 日志（2026-09-09 实抓）。
	if strings.HasSuffix(r.URL.Path, "/api/hello") {
		w.WriteHeader(200)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		s.fail(w, 400, "读请求体失败: "+err.Error())
		return
	}
	_ = r.Body.Close()

	tgt := parseTarget(r.URL.Path)

	// count_tokens：Anthropic 协议的私有端点，Claude Code 拿它算上下文水位。
	// 必须在 model 检查**之前**拦——有客户端发这个请求时不带 model 字段。
	// 上游有这个端点就转发拿真值（按 provider 学，见 forwardCountTokens），
	// 没有（聚合器 404）退回本地粗估。
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		reqID := atomic.LoadUint64(&s.requests)
		metrics.Default.Inc("count_tokens.total")
		if s.forwardCountTokens(w, r, body, tgt, reqID) {
			return
		}
		metrics.Default.Inc("count_tokens.local")
		s.handleCountTokens(w, reqID, body)
		return
	}

	// 不做任何 JSON 往返。只在原始字节里读出 model，后面也只替换那一段。
	inModel, ok := rewrite.TopLevelString(body, "model")
	if !ok || inModel == "" {
		// 带上方法与路径：这一类「形状不认识」的请求，没有路径就没法排查
		// 是哪个客户端、哪个端点发来的。
		s.fail(w, 400, fmt.Sprintf("请求体顶层没有 model 字符串字段（%s %s）",
			r.Method, r.URL.Path))
		return
	}
	norm := protocol.NormalizeRole(inModel)
	snap := s.snap()
	if snap == nil {
		s.fail(w, 400, "配置读不出 ← 跑 `newgate doctor`")
		return
	}
	st := snap.State
	stream0 := rewrite.TopLevelBool(body, "stream")
	reqID := atomic.LoadUint64(&s.requests)
	// 进来就记——否则「请求没到」和「到了在等上游」在日志里长得一样。
	// req= 是客户端请求体字节数：跟上游日志对账（300k compact 这种大输入）时
	// 靠它定位「同一发请求」，没有它就只剩 reqID 一个数，跨系统对不上。
	s.logf("[proxy] #%d ← %s stream=%v req=%d字节  开始", reqID, inModel, stream0, len(body))
	if st.DebugActive() {
		s.logf("[proxy] #%d 客户端请求 %s %s%s\n    body(%d字节): %s",
			reqID, r.Method, r.URL.Path, headerDump(r.Header), len(body),
			truncate(string(redact(body)), 4000))
	}

	// ---- per-agent 路由 + fallback 链 ----
	// 每个 agent 有自己的链头；URL 里的 /p/ 是本次调用的覆盖
	active := tgt.Profile
	if active == "" {
		active = st.ActiveFor(tgt.TaskCreate)
	}
	suffix := tgt.Suffix
	stream := stream0

	var steps []resolve.Step
	var skips []resolve.Skip
	tier := norm
	toolOrigin, hasToolOrigin := thinkcache.Default.ContinuationOrigin(body)
	// special 路由插件在常规解析前贡献结构化决策。热路径不知道具体插件名，
	// 也不解释它为什么改道；档位、覆盖链头、超时和可观测性都由插件声明。
	route, routed := special.Route(body, &special.Request{
		InModel: inModel, Tier: norm, Stream: stream0, Agent: tgt.TaskCreate,
	}, st)
	routeTier := ""
	if routed {
		routeTier = route.Tier
	}
	if routeTier != "" {
		opts := resolve.Opts{
			Active:    active,
			Available: health.Default.Available,
			Rank:      func(provider, model string) int { return health.Default.Rank(provider, model, len(body)) },
			MaxSteps:  st.Chain.Attempts(),
		}
		var rs []resolve.Step
		applied := false
		switch {
		case testChain != nil:
			rs = testChain(routeTier)
		case route.Head != nil:
			rs, skips, applied = resolve.OverrideChain("special:"+route.Plugin,
				*route.Head, routeTier,
				snap.Profiles, snap.Providers, opts)
			if !applied && route.OverrideFailNote != "" {
				s.logf("[proxy] #%d special_treatment %s: %s",
					reqID, route.Plugin, route.OverrideFailNote)
			}
		default:
			rs, skips = resolve.BuildChain(routeTier, snap.Profiles, snap.Providers, opts)
		}
		if len(rs) > 0 {
			steps, tier = rs, routeTier
			note := route.Note
			if applied && route.OverrideNote != "" {
				note = route.OverrideNote
			}
			if note != "" {
				s.logf("[proxy] #%d special_treatment %s: %s",
					reqID, route.Plugin, note)
			}
			if key := route.MetricKey(); key != "" {
				metrics.Default.Inc(key)
			}
		} else {
			// 插件要求的链为空时回落到常规解析：路由插件不能切断请求。
			routeTier = ""
			routed = false
		}
	}
	if routeTier == "" {
		if testChain != nil {
			steps = testChain(norm)
		} else {
			// 档位名走档位链；具体模型名反解回它所属档位，并把点名的模型放最前
			// （docs/04-configuration.md）——这支持「工具界面显示真实模型名」。
			steps, skips, tier = resolve.ResolveRequest(norm, active, snap.Profiles, snap.Providers, resolve.Opts{
				Active:    active,
				Available: health.Default.Available,
				Rank:      func(provider, model string) int { return health.Default.Rank(provider, model, len(body)) },
				MaxSteps:  st.Chain.Attempts(),
			})
		}
	}
	if len(steps) == 0 {
		s.logf("[proxy] #%d 无可用候选。跳过原因：%s", reqID, fmtSkips(skips))
		if tier == "" {
			s.fail(w, 404, fmt.Sprintf(
				"模型 %q 既不是已知档位（%s），也不在任何 profile 的绑定里。跑 `newgate tier` 看可用绑定",
				norm, strings.Join(domain.Roles, ", ")))
		} else {
			s.fail(w, 404, fmt.Sprintf(
				"档位 %q 在 profile %q 下没有可用候选（档位：%s）。跑 `newgate tier %s` 看每个候选为什么被跳过",
				tier, active, strings.Join(domain.Roles, ", "), tier))
		}
		return
	}
	deadline := start.Add(time.Duration(st.Chain.Budget()) * time.Millisecond)

	var lastMsg string
	var lastCode int
	var trail []string // 给 X-Newgate-Chain
	for i, a := range steps {
		isLast := i == len(steps)-1
		if i > 0 && time.Now().After(deadline) {
			metrics.Default.Inc("chain.budget_exhausted")
			s.logf("[proxy] #%d 链总预算 %dms 用尽，停在第 %d 步",
				reqID, st.Chain.Budget(), i)
			trail = append(trail, "budget-exhausted")
			isLast = true
		}
		// 纯字节手术：只替换顶层 model 的值，其余每个字节原样保留
		newBody, merr := rewrite.ReplaceTopLevelString(body, "model", a.Binding.Model)
		if merr != nil {
			s.fail(w, 500, "改写 model 失败: "+merr.Error())
			return
		}
		if hasToolOrigin {
			candidate := &special.Request{
				InModel: inModel, Tier: tier, Model: a.Binding.Model,
				Provider: a.Binding.Provider, BaseURL: a.Provider.Base(suffix),
				Protocol: a.Provider.Protocol, Path: suffix, Stream: stream,
				Agent: tgt.TaskCreate,
			}
			if res := special.RebaseToolLoop(newBody, toolOrigin.Provider, toolOrigin.Model,
				candidate, st.SpecialPluginOff); len(res.Notes) > 0 {
				for _, note := range res.Notes {
					s.logf("[proxy] #%d special_treatment %s", reqID, note)
				}
				if res.Changed {
					newBody = res.Body
				}
				for _, event := range res.Events {
					if key := event.MetricKey(); key != "" {
						metrics.Default.Inc(key)
					}
				}
			}
		}

		// tool schema 修补：只在真有东西要补时才重写 tools 这一个值，
		// messages / system / cache_control 仍然逐字节不动。
		if st.RepairEnabled() {
			if toolsRaw, ok := rewrite.TopLevelRaw(newBody, "tools"); ok {
				repaired, changes, rerr := schema.Repair(toolsRaw)
				switch {
				case rerr != nil:
					s.logf("[proxy] #%d tools 修补跳过（解析失败，按原样发）: %v", reqID, rerr)
				case len(changes) > 0:
					if nb, serr := rewrite.ReplaceTopLevelRaw(newBody, "tools", repaired); serr == nil {
						newBody = nb
						s.logf("[proxy] #%d 补了 %d 个 tool 的 \"required\": []（语义无操作，"+
							"为通过严格校验器）: %v", reqID, len(changes), changes)
					} else {
						s.logf("[proxy] #%d tools 回写失败，按原样发: %v", reqID, serr)
					}
				}
			}
		}

		// special_treatment：每家上游的怪癖补丁（gateway/special）。
		// 与 schema 修补的分工——那边是所有严格校验器都需要的通用修补，
		// 这边是「只有某家上游才需要」的，由插件自己 Match 认领。
		// （分类器改走 light 链的路由决策不在这——见上面 special.Route。）
		if st.SpecialEnabled() {
			res := special.Apply(newBody, &special.Request{
				InModel:  inModel,
				Tier:     tier,
				Model:    a.Binding.Model,
				Provider: a.Binding.Provider,
				// 按这次请求实际会去的 base 报——两种方言分家的上游
				// （provider.anthropic_url）要让插件看到真实那一个。
				BaseURL:  a.Provider.Base(suffix),
				Protocol: a.Provider.Protocol,
				Path:     suffix,
				Stream:   stream,
				Agent:    tgt.TaskCreate,
			}, st.SpecialPluginOff)
			counted := map[string]bool{}
			for _, n := range res.Notes {
				s.logf("[proxy] #%d special_treatment %s", reqID, n)
			}
			for _, event := range res.Events {
				key := event.MetricKey()
				if key != "" && !counted[key] {
					counted[key] = true
					metrics.Default.Inc(key)
				}
			}
			if res.Changed {
				newBody = res.Body
			}
		}

		target := a.Provider.URL(suffix)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		// 挂上客户端的 context：opencode 里按 ESC 取消时，上游请求也立刻中断，
		// 而不是让它跑完整个响应照样计费
		req, rerr := http.NewRequestWithContext(r.Context(), r.Method, target,
			bytes.NewReader(newBody))
		if rerr != nil {
			s.fail(w, 500, rerr.Error())
			return
		}
		s.dump(reqID, i, body, newBody)
		if st.DebugActive() {
			s.logf("[proxy] #%d 发往上游 %s\n    body(%d字节, 与原文差 %+d): %s",
				reqID, target, len(newBody), len(newBody)-len(body),
				truncate(string(redact(newBody)), 4000))
		}
		copyHeaders(req.Header, r.Header)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Del("Content-Length")
		setAuth(req.Header, a.Provider)

		// 流式不能设总超时（长响应会被砍断），但必须限制首字节等待时间，
		// 否则上游装死就永久挂住。ResponseHeaderTimeout 正好只管到响应头。
		// 紧上限只给**分类器**（它挡在交互通路上，挂住 = 冻住会话，靠
		// system marker 精确认出）；其他请求——包括 /compact 这种 500KB+
		// 的非流式大输入——一律标准 150s：大 prefill 合法地慢，一刀切
		// 紧超时只会在链上连环掐死（2026-09-09 实抓教训）。全部从
		// state.json 的 timeouts 热加载，误杀率看 newgate metrics。
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = st.Timeouts.FirstByte()
		if routed && route.FirstByteTimeout > 0 {
			tr.ResponseHeaderTimeout = route.FirstByteTimeout
		}
		client := &http.Client{Transport: tr, Timeout: func() time.Duration {
			if stream {
				return 0
			}
			return st.Timeouts.Total()
		}()}

		attemptStart := time.Now()
		resp, derr := client.Do(req)
		attemptTTFT := time.Since(attemptStart)
		// 路由串按**实际发出的** body model 报——special 插件可能已把
		// mid 切成 light（claude-bg 的分类器切档），拿链步的模型打日志
		// 会把人引去查错方向（2026-09-09：日志说 glm-5.3，dump 说 air）。
		sentModel := a.Binding.Model
		if m, has := rewrite.TopLevelString(newBody, "model"); has && m != "" {
			sentModel = m
		}
		routeStr := fmt.Sprintf("%s -> %s/%s", inModel, a.Binding.Provider, sentModel)

		if derr != nil {
			// 客户端自己走了（按 ESC、关窗口、客户端侧超时）不是上游的错。
			//
			// 不加这一闸的后果实测过：一次取消会被当成 smt-claude 连接失败 →
			// 记一次失败 → 沿链走到 smt-deepseek，用的还是那个已经死掉的
			// context，于是**每个候选都瞬间失败**，一次 ESC 就把整条链上所有
			// provider 的熔断器全打开（日志里三个 provider 一起「暂时摘掉」）。
			// 之后真正的请求反而没候选可用，回 502——用户看到的是「取消一下
			// 之后全挂了」，根本查不到源头。
			//
			// 所以：不记失败、不开熔断、不往下走链、也不写 502（对面已经没人了）。
			if cerr := r.Context().Err(); cerr != nil {
				metrics.Default.Inc("client.cancel")
				s.logf("[proxy] #%d %s 客户端在连接阶段就取消了（%v），停止整条链",
					reqID, routeStr, cerr)
				return
			}
			if strings.Contains(derr.Error(), "timeout awaiting response headers") {
				if stream {
					metrics.Default.Inc("timeout.first_byte.stream")
				} else {
					metrics.Default.Inc("timeout.first_byte.non_stream")
				}
			}
			opened := health.Default.RecordFailure(a.Binding.Provider, a.Binding.Model)
			if opened {
				metrics.Default.Inc("breaker.opened")
			}
			atomic.AddUint64(&s.failures, 1)
			hint := ""
			if strings.Contains(derr.Error(), "timeout awaiting response headers") {
				waitLimit := st.Timeouts.FirstByte()
				if routed && route.FirstByteTimeout > 0 {
					waitLimit = route.FirstByteTimeout
				}
				hint = fmt.Sprintf("  [首字节超过 %v——上游装死或排队]", waitLimit)
			}
			s.logf("[proxy] #%d %s 连接失败: %v%s%s", reqID, routeStr, derr,
				breakerNote(opened, a.Binding.Provider), hint)
			lastMsg, lastCode = fmt.Sprintf("上游 %s 连接失败: %v", a.Binding.Provider, derr), 502
			trail = append(trail, fmt.Sprintf("%s(conn)", a.Binding))
			if !isLast {
				metrics.Default.Inc("chain.step_failed")
				s.logf("[proxy] → 沿链下一步: %s", steps[i+1])
				continue
			}
			s.fail(w, 502, lastMsg)
			return
		}

		// 可转移的失败：还没往客户端写任何字节，安全
		if resp.StatusCode >= 400 && health.ShouldAdvance(resp.StatusCode, st.Chain.FallbackOn400) && !isLast {
			body, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			opened := health.Default.RecordFailure(a.Binding.Provider, a.Binding.Model)
			if opened {
				metrics.Default.Inc("breaker.opened")
			}
			atomic.AddUint64(&s.failures, 1)
			s.logf("[proxy] %s -> %d%s  上游说: %s", routeStr, resp.StatusCode,
				breakerNote(opened, a.Binding.Provider), trim(string(body)))
			s.learnQuirks(reqID, a.Binding.Provider, a.Binding.Model, resp.StatusCode, body)
			s.logf("[proxy] → 沿链下一步: %s", steps[i+1])
			metrics.Default.Inc("chain.step_failed")
			lastMsg, lastCode = trim(string(body)), resp.StatusCode
			trail = append(trail, fmt.Sprintf("%s(%d)", a.Binding, resp.StatusCode))
			continue
		}

		// 定案：把这个响应交给客户端
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			health.Default.RecordFailure(a.Binding.Provider, a.Binding.Model)
			atomic.AddUint64(&s.failures, 1)

			// 错误响应体一般不大，整个读出来当证据，再原样转给客户端
			eb, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 256*1024))
			base := s.saveErrEvidence(reqID, resp.StatusCode, body, newBody, eb,
				r.Header, resp.Header, routeStr)
			s.logf("[proxy] #%d 上游 %d，完整证据已存 %s.*", reqID, resp.StatusCode, base)
			s.learnQuirks(reqID, a.Binding.Provider, a.Binding.Model, resp.StatusCode, eb)
			if isReasoningPassthroughError(eb) {
				// 专属标记：这类 400 不是客户端 schema 错，是我们补的思考内容
				// 被上游严格节点拒了（DeepSeek 灰度），要能一眼 grep 出来。
				s.logf("[reasoning-400] #%d %s 上游拒收思考内容回传（证据 %s.*）",
					reqID, a.Binding.String(), filepath.Base(base))
				// 现场单独存档：这类 400 偶发又致命，dump 目录的 req-*/err-*
				// 滚动清理会把它挤掉，所以另存一份到不参与滚动清理的专用目录，
				// 并附逐条 reasoning 审计（哪几条补了占位符）。
				if rdir := s.saveReasoningEvidence(reqID, body, newBody, eb,
					r.Header, resp.Header, routeStr); rdir != "" {
					s.logf("[reasoning-400] #%d 现场已存档 %s/", reqID, rdir)
				}
			}
			s.logf("[proxy] #%d 上游原文: %s", reqID, truncate(string(redact(eb)), 2000))
			s.logf("[proxy] #%d 我们发出的 body(%d字节): %s", reqID, len(newBody),
				truncate(string(redact(newBody)), 4000))

			for k, vs := range resp.Header {
				if respHopHeaders[http.CanonicalHeaderKey(k)] || http.CanonicalHeaderKey(k) == "Content-Length" {
					continue
				}
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Newgate-Route", routeStr)
			w.Header().Set("X-Newgate-Profile", a.Profile)
			w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
			w.Header().Set("X-Newgate-Evidence", filepath.Base(base))
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(eb)
			return
		}
		health.Default.ObserveSuccess(a.Binding.Provider, a.Binding.Model, len(newBody), attemptTTFT)
		health.Default.RecordSuccess(a.Binding.Provider, a.Binding.Model)
		if i > 0 {
			metrics.Default.Inc("chain.failover")
		}
		if st.DebugActive() {
			s.logf("[proxy] #%d 上游响应头 %d%s", reqID, resp.StatusCode, headerDump(resp.Header))
		}
		s.logf("[proxy] #%d %s  %s  %d  首字节%dms  stream=%v  profile=%s%s",
			reqID, routeStr, suffix, resp.StatusCode, time.Since(start).Milliseconds(),
			stream, a.Profile, map[bool]string{true: "  (已转移)"}[i > 0])

		for k, vs := range resp.Header {
			if respHopHeaders[http.CanonicalHeaderKey(k)] {
				continue // Transfer-Encoding / Connection 由 Go 的 server 自己管
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("X-Newgate-Route", routeStr)
		w.Header().Set("X-Newgate-Profile", a.Profile)
		w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
		if i > 0 {
			// 转移了必须明确告知，绝不静默（docs/05-gateway.md）
			// HTTP header 只能装 latin-1，中文会乱码——只放 ASCII，详情在日志里
			w.Header().Set("X-Newgate-Failover", fmt.Sprintf("%s -> %s (upstream %d; see: newgate logs)",
				steps[0].Profile, a.Profile, lastCode))
		}
		w.WriteHeader(resp.StatusCode)

		// 逐块转发。响应体一个字节都不改，也绝不缓冲整个响应。
		//
		// 旁路挂一个观测者，把上游吐出来的推理内容记进 thinkcache，供下一轮
		// 补回去（客户端会把它剥掉，见 modules/deepseek/st-reasoning.go）。
		// 它是**纯只读**的：拿到的是已经写给客户端的那一份字节，不参与转发，
		// 看错了最坏结果是这轮没缓存上。
		flusher, canFlush := w.(http.Flusher)
		ob := thinkcache.NewObserver()
		var nonStream []byte // 非流式：整个 body 才能解析，攒完再看（有上限）
		buf := make([]byte, 32*1024)
		var chunks int
		var bytesOut int64
		for {
			n, rderr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					metrics.Default.Inc("client.cancel")
					s.logf("[proxy] #%d 客户端断开（已转发 %d 块 / %d 字节）: %v",
						reqID, chunks, bytesOut, werr)
					return
				}
				if stream {
					ob.Write(buf[:n])
				} else if len(nonStream) < 8<<20 {
					nonStream = append(nonStream, buf[:n]...)
				}
				chunks++
				bytesOut += int64(n)
				if canFlush {
					flusher.Flush() // 每读到就吐，不等缓冲区满
				}
			}
			if rderr != nil {
				switch {
				case rderr == io.EOF:
					if !stream && len(nonStream) > 0 {
						ob.ObserveBody(nonStream)
					}
					origin := thinkcache.Origin{
						Profile: a.Profile, Provider: a.Binding.Provider, Model: a.Binding.Model,
					}
					if nb, nk := ob.CommitWithOrigin(thinkcache.Default, origin); nb > 0 {
						// 只打字节数和 key 数，绝不打内容
						s.logf("[proxy] #%d 记下本轮推理内容 %d 字节 / %d 个 key，"+
							"下一轮替客户端补回去", reqID, nb, nk)
					}
					if stream {
						s.logf("[proxy] #%d 流正常结束：%d 块 / %d 字节 / 总 %dms",
							reqID, chunks, bytesOut, time.Since(start).Milliseconds())
					} else {
						s.logf("[proxy] #%d 非流式响应结束：%d 字节 / 总 %dms",
							reqID, bytesOut, time.Since(start).Milliseconds())
					}
				case r.Context().Err() != nil:
					metrics.Default.Inc("client.cancel")
					s.logf("[proxy] #%d 客户端取消，已掐断上游（省下后续 token）", reqID)
				default:
					s.logf("[proxy] #%d 上游断流（已转发 %d 块 / %d 字节）: %v",
						reqID, chunks, bytesOut, rderr)
				}
				return
			}
		}
	}
}