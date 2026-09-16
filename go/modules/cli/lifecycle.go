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

func cmdStart(agents agentapi.AgentCatalog, force bool) int {
	if _, err := os.Stat(paths.ProvidersFile()); os.IsNotExist(err) {
		fmt.Println(style.Dim("首次运行，初始化配置"))
		if _, err := store.Init(false); err != nil {
			return die(70, err.Error())
		}
	}
	if i := daemon.Running(); i != nil {
		fmt.Println(style.Item(style.OK, fmt.Sprintf("代理已在运行   pid %d · 127.0.0.1:%d", i.PID, i.Port)))
		return 0
	}

	// 令牌先于 Spawn 落盘：daemon 一起来就要能验 /__newgate/stop。
	// 也是为了防竞态——下面 Spawn 之后 st 还会被 SaveState 写回，
	// 先确保 st 里带着令牌，写回就不会把 daemon 已生成的令牌冲掉。
	st := store.EnsureControlToken()
	// 没 key 就别接管——接管了每个请求都是错误，而用户的配置已经被改了
	if probs := activeProblems(st); len(probs) > 0 && !force {
		fmt.Fprintf(os.Stderr, "newgate: 配置不可用，拒绝接管\n")
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "  %s %s\n", style.Mark(style.Bad), p)
		}
		fmt.Fprintf(os.Stderr, "%s\n", style.Hint("填 key 进 "+paths.ProvidersFile()+"，或设对应的环境变量"))
		fmt.Fprintf(os.Stderr, "%s\n", style.Hint("确认：newgate doctor    强行接管：newgate start --force"))
		return 65
	}

	info, err := daemon.Spawn(st.Port)
	if err != nil {
		return die(70, "启动守护进程失败: "+err.Error())
	}
	ok := false
	for k := 0; k < 60; k++ {
		if pingProxy(st.Port) {
			ok = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !ok {
		// 区分「进程没起来」和「起来了但我们连不上」——后者绝不能杀进程
		if httpx.TCPAlive("127.0.0.1", st.Port, time.Second) {
			fmt.Fprintf(os.Stderr,
				"newgate: 端口 %d 在听但 HTTP 探活失败，代理进程保留。\n"+
					"  最常见原因：出站代理把 loopback 请求劫走了。跑 newgate doctor。\n", st.Port)
		} else {
			fmt.Fprintf(os.Stderr, "newgate: 代理没起来（端口 %d 没在听），看日志 %s\n",
				st.Port, paths.LogFile())
			_, _ = daemon.Stop()
			return 70
		}
	}
	fmt.Println(style.Item(style.OK, fmt.Sprintf("代理已启动   pid %d · 127.0.0.1:%d · profile %s",
		info.PID, st.Port, st.DefaultProfile)))

	// 全面接管：所有没被用户显式 off 掉的 agent，各按自己的机制插上。
	fmt.Println()
	rs := takeover.OnAll(st.Port)
	printResults(rs)
	for _, r := range rs {
		if r.Mechanism == takeover.MechShim && r.Err == nil && !r.Skipped && len(r.Lines) > 0 {
			warnShellEnvConflict(agents, r.Agent)
		}
	}

	st.TakenOver = true
	_ = store.SaveState(st)
	fmt.Println()
	fmt.Println(style.Hint("配置改动 1 秒内自动热更新，无需重启"))
	fmt.Println(style.Hint("原配置备份 " + paths.BackupDir() + "/original/"))
	return 0
}

// printResults 把接管/释放结果打成人话。
//
// 三种标记不能混：✓ 真的接管上了，· 有意跳过，✗ 出错了。agent 名补到固定
// 宽度，续行对齐到说明列——一次接管多个 agent 时才扫得动。
func printResults(rs []takeover.Result) {
	const nameW = 10
	for _, r := range rs {
		switch {
		case r.Err != nil:
			fmt.Fprintln(os.Stderr, style.Item(style.Bad, style.Pad(r.Agent, nameW)+r.Err.Error()))
		case len(r.Lines) == 0:
			continue
		default:
			mark := style.OK
			if r.Skipped {
				mark = style.Skip
			}
			fmt.Println(style.Item(mark, style.Pad(r.Agent, nameW)+r.Lines[0]))
			for _, l := range r.Lines[1:] {
				fmt.Println("    " + style.Pad("", nameW) + style.Dim(l))
			}
		}
		for _, w := range r.Warn {
			fmt.Println(style.Bullet(style.Mark(style.Warn) + " " + w))
		}
	}
}

// cmdTakeover / cmdRelease 单独接管或释放一个 agent。
//
// 用户不该关心机制：claude 靠 PATH shim、opencode 靠改配置文件，这是我们的
// 实现细节。对外只有「接管」和「释放」，机制由 runtime/takeover 挑。
func cmdTakeover(agents agentapi.AgentCatalog, agent string) int {
	if agent == "" {
		return die(64, "要接管谁？例：newgate on claude")
	}
	st := store.LoadState()
	r := takeover.On(agent, st.Port)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	printResults([]takeover.Result{r})
	if r.Mechanism == takeover.MechShim {
		warnShellEnvConflict(agents, agent)
	}
	if daemon.Running() == nil {
		fmt.Println(style.Hint("代理未运行 · newgate start 之后才生效"))
	}
	return 0
}