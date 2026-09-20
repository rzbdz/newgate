package gateway

// 本文件是**守护进程的主循环**：`newgate __serve`（由 daemon.Spawn 拉起）。
//
// **为什么它住在这里**（2026-09-18）：它在跑的就是数据面本身——配置热更新的原子
// 换页、thinkcache 的磁盘冷层、转发服务（forward.New）的启停、优雅交接的排空。
// 这些没有一样是「命令行」的事。它留在 modules/cli 的时候，界面为了跑起来得认识
// forward / thinkcache / 健康表 / config,store / runtime,daemon ——界面的依赖表
// 里于是有一半是数据面。
//
// 搬过来之后：界面不认识进程生命周期，进程也不认识界面。`__serve` 是网关自己
// 在 Start 里注册进界面的命令（本模块对 ui 是弱依赖 Optional(cli)），用户看不见
// 它（故意没有 HelpLine）。
//
// 依赖方向：gateway → cli/extension（叶子契约），不是 → modules/cli。

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/lib/buildinfo"
	"github.com/rzbdz/newgate/lib/logx"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/modules/gateway/forward"
	"github.com/rzbdz/newgate/modules/gateway/policy"
	"github.com/rzbdz/newgate/modules/gateway/thinkcache"
	"github.com/rzbdz/newgate/modules/runtime/daemon"
)

// serveCommand 是守护进程本体：`newgate __serve`。
//
// **故意没有 HelpLine**：它是内部入口，用户不该在 help 里看到它，也不该手敲。
//
// 它只带 filters（数据面策略账本）进去，不带任何具体策略：账本在 Bind 期就存在
// （gateway 的 New 里建），贡献者在各自 Start 期往里写，数据面在 Serve 期读。
type serveCommand struct{ filters *policy.Registry }

var (
	_ cliapi.Command  = (*serveCommand)(nil)
	_ cliapi.Unstyled = (*serveCommand)(nil)
)

func (serveCommand) Names() []string { return []string{"__serve"} }

// Unstyled：守护进程本体，输出是它自己的日志流（见 cliapi.Unstyled）。
func (serveCommand) Unstyled([]string) bool { return true }

func (c serveCommand) Run(_ cliapi.Host, args []string) int {
	return Serve(c.filters, intFlag(args, "--port", 0))
}

// intFlag 取 `--名字 N` 或 `--名字=N` 里的整数，没有给默认值。
func intFlag(args []string, name string, def int) int {
	for i, a := range args {
		v := ""
		switch {
		case a == name && i+1 < len(args):
			v = args[i+1]
		case strings.HasPrefix(a, name+"="):
			v = strings.TrimPrefix(a, name+"=")
		default:
			continue
		}
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// Serve 是守护进程的主循环。
//
// 热更新在这里落地（服务稳定性的地基）：watcher 后台监测配置目录，
// 变了就原子换页。已经在跑的 agent **完全不受影响**——改 claude 的
// profile 不会打断正在跑的 opencode，因为：
//   - 每个 agent 读自己的绑定（per-agent profile）
//   - 换页是原子指针替换，不存在「读到一半配置变了」的中间态
//   - 新配置加载失败时**保留旧快照**，正在跑的一切继续工作
//
// 依赖方向：数据面只认识**策略账本**（policy.Registry），不认识任何一位策略。
// 账本由 gateway 的 New 建好（Bind 期），策略在各自 Start 期注册进来。
func Serve(filters *policy.Registry, port int) int {
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
		// 抢到锁之后**自己**写 pidfile：只有持有监听权的进程才该出现在那里。
		// 父进程代写会被这一步之后的「陈旧锁清理」删掉（AcquireLock 里那句
		// RemovePid），而再没有人补回来——2026-09-18 两次现场的完整链条见
		// daemon.WriteOwn。写不出去只警告：代理照常服务，只是 status/metrics
		// 会看不到它（那时候 doctor 的「守护进程」一项会说出来）。
		if err := daemon.WriteOwn(port); err != nil {
			lg.Printf("pidfile 写不出去（status/metrics 会看不到本进程）: %v", err)
		}
	} else {
		lg.Printf("优雅交接：接管父进程的监听 socket")
	}

	// 控制令牌必须先于 watcher 落盘：别的用户 `newgate stop` 发不出信号，
	// 只能走 /__newgate/stop，daemon 一启动就得能验它。这里是最后兜底
	// （cmdStart / launch.Launch 在 Spawn 前已经各兜一次，正常早就有）。
	if st, err := store.EnsureControlToken(); err != nil {
		// daemon 照常起：没有令牌只是跨用户停机不可用，不该拦下整个代理。
		lg.Printf("控制令牌写不出去（跨用户停机不可用）: %v", err)
	} else if st.ControlToken != "" {
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
	// 纯内存（重启前那几轮的原文找不回来了），落盘永远不反过来搞挂代理。
	if err := thinkcache.AttachDisk(paths.ThinkCacheFile(), 100<<20); err != nil {
		lg.Printf("thinkcache 落盘关闭（继续纯内存）: %v", err)
	}
	// 冷层跑起来之后的失败（压实丢记录、刷盘失败、冷层停用）必须说出来：
	// 用户能观察到的现象是「重启后推理找不回来了」，而那正是最难反推原因的一类。
	thinkcache.SetErrorHandler(func(err error) {
		lg.Printf("[thinkcache] %v", err)
	})
	srv := forward.New(port, lg, watcher, filters)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// requested 记录「这次退出是有人要求的」，只影响退出码。
	//
	// 停机路径**只有一个出口**：下面的 goroutine 只负责 `srv.Shutdown()`
	// 让 Serve() 返回，pid/lock 的清理和退出码一律由 srv.Start() 之后那段
	// 统一做。曾经三个出口各自 `RemoveLock + RemovePid + os.Exit(0)`，和主
	// 路径的 `return 70` 抢跑：实测（2026-09-17，mock/e2e_claude.sh 第 11 章）
	// 主路径先跑完，`RemoveLock` 有 defer 兜住、`RemovePid` 没有，于是
	// .newgate.pid 留在磁盘上——下次 start/restart 就会读到一个死 pid。
	var requested int32

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
			atomic.StoreInt32(&requested, 1)
			srv.Shutdown()
			return
		}
	}()

	// 控制端点停机（别的用户 `newgate stop`）：发不出信号的用户靠它停机。
	// 同样只关 listener，清理交给下面统一做。
	go func() {
		<-srv.StopRequested()
		if srv.Draining() {
			// 排空期收到停机：同样不许动新进程的 pid/lock
			lg.Printf("排空期收到控制停机，直接退出（不动 pid/lock）")
			os.Exit(0)
		}
		lg.Printf("控制停机（令牌校验通过），退出")
		atomic.StoreInt32(&requested, 1)
		srv.Shutdown()
	}()

	lg.Printf("newgate %s (构建于 %s) 启动，默认 profile=%s，配置热更新已开启",
		buildinfo.Version(), buildinfo.BuildTimeDisplay(), watcher.Current().State.DefaultProfile)
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
		// 唯一出口：进程要走了，pid/lock 是它自己的，必须一起带走。
		// defer 的 RemoveLock 只管 lock，pid 得在这里补上。
		daemon.RemoveLock()
		daemon.RemovePid()
		if atomic.LoadInt32(&requested) == 1 {
			return 0
		}
		return 70
	}
	return 0
}
