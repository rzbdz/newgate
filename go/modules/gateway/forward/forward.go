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