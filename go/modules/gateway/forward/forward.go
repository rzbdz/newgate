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