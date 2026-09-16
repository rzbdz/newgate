package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/lib/httpx"
	"github.com/rzbdz/newgate/go/lib/logx"
	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/store"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/gateway/forward"
	"github.com/rzbdz/newgate/go/modules/gateway/thinkcache"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
	"github.com/rzbdz/newgate/go/modules/runtime/takeover"
)

// Serve 是守护进程的主循环。
//
// 热更新在这里落地（服务稳定性的地基）：watcher 后台监测配置目录，
// 变了就原子换页。已经在跑的 agent **完全不受影响**——改 claude 的
// profile 不会打断正在跑的 opencode，因为：
//   - 每个 agent 读自己的绑定（per-agent profile）
//   - 换页是原子指针替换，不存在「读到一半配置变了」的中间态
//   - 新配置加载失败时**保留旧快照**，正在跑的一切继续工作
func Serve(port int) int {
	rot, rerr := logx.New(paths.LogFile(), 16<<20, 3) // 16MB × 4 份
	var lg *log.Logger
	if rerr != nil {
		lg = log.New(os.Stdout, "", log.LstdFlags)
		lg.Printf("日志轮转初始化失败，退回 stdout: %v", rerr)
	} else {
		defer rot.Close()
		lg = log.New(rot, "", log.LstdFlags)
	}

	// 优雅交接进来的进程：pid/lock 已由父进程改写到自己名下（AdoptRuntime），
	// 此时抢锁会读到自己的 pid 而误判「已在运行」。父进程就是担保人，跳过。
	inherited := os.Getenv("NEWGATE_LISTENER_FD") != ""
	if !inherited {
		if err := daemon.AcquireLock(); err != nil {
			lg.Printf("抢锁失败: %v", err)
			return 69
		}
		defer daemon.RemoveLock()
	} else {
		lg.Printf("优雅交接：接管父进程的监听 socket")
	}

	// 控制令牌必须先于 watcher 落盘：别的用户 `newgate stop` 发不出信号，
	// 只能走 /__newgate/stop，daemon 一启动就得能验它。这里是最后兜底
	// （cmdStart / launch.Launch 在 Spawn 前已经各兜一次，正常早就有）。
	if st := store.EnsureControlToken(); st.ControlToken != "" {
		lg.Printf("控制端点 /__newgate/stop 已就绪（跨用户停机可用）")
	}

	watcher, err := store.NewWatcher(time.Second)
	if err != nil {
		lg.Printf("配置加载失败: %v", err)
		return 65
	}
	watcher.OnChange(func(old, nw *store.Snapshot) {
		changes := store.DiffSummary(old, nw)
		if len(changes) == 0 {
			lg.Printf("[config] 配置已重载（第 %d 代），无结构性变化", watcher.Generation())
			return
		}
		lg.Printf("[config] 配置热更新（第 %d 代）：%v —— 正在跑的会话不受影响",
			watcher.Generation(), changes)
	})
	watcher.OnError(func(e error) {
		// 关键：坏配置不替换快照。宁可用旧配置继续跑，也不能因为一次
		// 手误让所有 agent 同时失效。
		lg.Printf("[config] ⚠ 新配置加载失败，**继续使用上一份可用配置**: %v", e)
	})
	watcher.Start()
	defer watcher.Close()

	if port <= 0 {
		port = watcher.Current().State.Port
	}
	// thinkcache 落盘冷层：daemon 重启后找回上一进程的推理内容。失败就降级
	// 纯内存（补不回来的轮次用占位符兜底），落盘永远不反过来搞挂代理。
	if err := thinkcache.AttachDisk(paths.ThinkCacheFile(), 100<<20); err != nil {
		lg.Printf("thinkcache 落盘关闭（继续纯内存）: %v", err)
	}
	srv := forward.New(port, lg, watcher)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for s := range sig {
			if s == syscall.SIGHUP {
				// SIGHUP 是「重读配置」的传统语义
				lg.Printf("收到 SIGHUP，强制重载配置")
				watcher.Reload(true)
				continue
			}
			if srv.Draining() {
				// 排空期被信号打断：pid/lock 已是新进程的，清不得
				lg.Printf("排空期收到 %v，直接退出（不动 pid/lock）", s)
				os.Exit(0)
			}
			lg.Printf("收到 %v，退出", s)
			srv.Shutdown()
			daemon.RemoveLock()
			daemon.RemovePid()
			os.Exit(0)
		}
	}()

	// 控制端点停机（别的用户 `newgate stop`）：效果和信号一样——
	// 关 listener、清 pid/lock、退干净。cmdStop 后半段还要 LoadState，
	// 所以必须 os.Exit 而不是只 return，别让 deferred 副作用拖泥带水。
	go func() {
		<-srv.StopRequested()
		if srv.Draining() {
			// 排空期收到停机：同样不许动新进程的 pid/lock
			lg.Printf("排空期收到控制停机，直接退出（不动 pid/lock）")
			os.Exit(0)
		}
		lg.Printf("控制停机（令牌校验通过），退出")
		srv.Shutdown()
		daemon.RemoveLock()
		daemon.RemovePid()
		os.Exit(0)
	}()

	lg.Printf("newgate %s (构建于 %s) 启动，默认 profile=%s，配置热更新已开启",
		Version, buildTimeDisplay(), watcher.Current().State.DefaultProfile)
	if err := srv.Start(); err != nil {
		// 优雅交接的排空：listener 已移交新进程，Serve 因此返回——但这
		// 不是退出的时候。等在途请求流完（Drained），再直接退（os.Exit
		// 跳过 defer 的 RemoveLock：pid/lock 已是 新进程的）。
		if srv.Draining() {
			lg.Printf("监听 socket 已移交新进程，等待在途请求排空（上限 10 分钟）")
			<-srv.Drained()
			lg.Printf("排空完成，旧进程功成身退")
			os.Exit(0)
		}
		lg.Printf("代理退出: %v", err)
		return 70
	}
	return 0
}