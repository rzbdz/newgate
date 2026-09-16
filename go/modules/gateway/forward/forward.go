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