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

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/gateway/forward"
	"github.com/rzbdz/newgate/go/internal/platform/httpx"
	"github.com/rzbdz/newgate/go/internal/platform/logx"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/runtime/takeover"
	"github.com/rzbdz/newgate/go/internal/store"
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
		Version, pretty(BuildTime), watcher.Current().State.DefaultProfile)
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

func cmdStart(force bool) int {
	if _, err := os.Stat(paths.ProvidersFile()); os.IsNotExist(err) {
		fmt.Println("首次运行，先初始化配置…")
		if _, err := store.Init(false); err != nil {
			return die(70, err.Error())
		}
	}
	if i := daemon.Running(); i != nil {
		fmt.Printf("已在运行 (pid %d, 端口 %d)\n", i.PID, i.Port)
		return 0
	}

	// 令牌先于 Spawn 落盘：daemon 一起来就要能验 /__newgate/stop。
	// 也是为了防竞态——下面 Spawn 之后 st 还会被 SaveState 写回，
	// 先确保 st 里带着令牌，写回就不会把 daemon 已生成的令牌冲掉。
	st := store.EnsureControlToken()
	// 没 key 就别接管——接管了每个请求都是错误，而用户的配置已经被改了
	if probs := activeProblems(st); len(probs) > 0 && !force {
		fmt.Fprintf(os.Stderr, "newgate: 当前配置还不能用，拒绝接管：\n")
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "  ✗ %s\n", p)
		}
		fmt.Fprintf(os.Stderr, "\n修法：把 key 填进 %s，或设对应的环境变量。\n",
			paths.ProvidersFile())
		fmt.Fprintf(os.Stderr, "然后 newgate doctor 确认。（强行接管：newgate start --force）\n")
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
	fmt.Printf("✓ 代理已启动  pid=%d  127.0.0.1:%d  默认 profile=%s\n",
		info.PID, st.Port, st.DefaultProfile)

	// 全面接管：所有没被用户显式 off 掉的 agent，各按自己的机制插上。
	fmt.Println()
	rs := takeover.OnAll(st.Port)
	printResults(rs)
	for _, r := range rs {
		if r.Mechanism == takeover.MechShim && r.Err == nil && !r.Skipped && len(r.Lines) > 0 {
			warnShellEnvConflict(r.Agent)
		}
	}

	st.TakenOver = true
	_ = store.SaveState(st)
	fmt.Printf("\n配置改动会自动热更新，不需要重启代理。\n")
	fmt.Printf("原配置备份在 %s/original/\n", paths.BackupDir())
	return 0
}

// printResults 把接管/释放结果打成人话。
// 三种标记不能混：✓ 真的接管上了，· 有意跳过，✗ 出错了。
func printResults(rs []takeover.Result) {
	for _, r := range rs {
		switch {
		case r.Err != nil:
			fmt.Fprintf(os.Stderr, "✗ %-10s %v\n", r.Agent, r.Err)
		case len(r.Lines) == 0:
			continue
		default:
			mark := "✓"
			if r.Skipped {
				mark = "·"
			}
			fmt.Printf("%s %-10s %s\n", mark, r.Agent, r.Lines[0])
			for _, l := range r.Lines[1:] {
				fmt.Printf("             %s\n", l)
			}
		}
		for _, w := range r.Warn {
			fmt.Printf("  ⚠ %s\n", w)
		}
	}
}

// cmdTakeover / cmdRelease 单独接管或释放一个 agent。
//
// 用户不该关心机制：claude 靠 PATH shim、opencode 靠改配置文件，这是我们的
// 实现细节。对外只有「接管」和「释放」，机制由 runtime/takeover 挑。
func cmdTakeover(agent string) int {
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
		warnShellEnvConflict(agent)
	}
	if daemon.Running() == nil {
		fmt.Println("\n注意：代理还没跑。newgate start 起来它才真的能用。")
	}
	return 0
}

func cmdRelease(agent string) int {
	if agent == "" {
		return die(64, "要释放谁？例：newgate off claude")
	}
	r := takeover.Off(agent)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	if len(r.Lines) == 0 {
		fmt.Printf("- %s 本来就没被接管\n", agent)
	} else {
		printResults([]takeover.Result{r})
	}
	fmt.Printf("%s 已恢复直连；以后 newgate start 也不会再接管它（newgate on %s 可恢复）\n",
		agent, agent)
	if r.Mechanism == takeover.MechShim {
		fmt.Println("  当前 shell 可能缓存了路径，`hash -r`（zsh: `rehash`）一下")
	}
	return 0
}

func cmdStop() int {
	// 顺序很重要：先把 PATH shim 摘掉。否则 `claude` 这类命令仍会命中
	// ~/.config/newgate/bin/claude → newgate，而 wrapper 会**懒启动代理**，
	// stop 等于没停——这是「stop 了还在走 newgate」的直接原因。
	//
	// OffAll 不改接管意愿：下次 start 会把这些原样插回去。
	printResults(takeover.OffAll())

	pid, err := daemon.Stop()
	if err != nil {
		return die(70, err.Error())
	}
	if pid > 0 {
		fmt.Printf("✓ 代理已停 (pid %d)\n", pid)
	} else {
		fmt.Println("- 代理本来没在跑")
	}

	st := store.LoadState()
	st.TakenOver = false
	_ = store.SaveState(st)

	fmt.Println("\nnewgate 已完全退出，所有工具恢复直连。")
	fmt.Println("  PATH 里那行仍指向空的 shim 目录，无害；彻底清掉用 `newgate shim uninstall`。")
	return 0
}

// cmdRestart 重启代理并保留接管现场。
//
// 优先走优雅交接（nginx upgrade 语义）：旧 daemon 把监听 socket 移交给
// 新二进制，在途请求流完为止——正穿行在代理里的会话（比如正在开发
// newgate 的 Claude Code）完全不受影响，接管状态也不动。
// 运行中的是旧版 daemon（没有交接能力）时退回 stop+start：有短暂断流
// 窗口，会提示一句。
func cmdRestart(force bool) int {
	if tryHandoff() {
		return 0
	}
	cmdStop()
	fmt.Println()
	return cmdStart(force)
}

// tryHandoff 让运行中的 daemon 把监听 socket 交接给磁盘上的新二进制。
// 成功（已完全就位）返回 true；不可行时打印原因并返回 false，由调用方
// 退回 stop+start。
func tryHandoff() bool {
	i := daemon.Running()
	if i == nil {
		return false // 没在跑，restart 退化为 start
	}
	st := store.LoadState()
	if st.ControlToken == "" {
		fmt.Println("· daemon 还没有控制令牌（升级前启动的），退回 stop+start")
		return false
	}
	port := i.Port
	if port <= 0 {
		port = st.Port
	}
	// 先探能力再动手：旧版 daemon 没注册 /__newgate/upgrade，盲发会被
	// catch-all 转发给上游。status 的 handoff 字段是无副作用的探针。
	supports, err := handoffSupported(port)
	if err != nil || !supports {
		if err != nil {
			fmt.Printf("· 探测 daemon 交接能力失败（%v），退回 stop+start\n", err)
		} else {
			fmt.Println("· 运行中的是旧版 daemon（不支持优雅交接），退回 stop+start")
		}
		return false
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/__newgate/upgrade", port), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+st.ControlToken)
	// 旧 daemon 要等新进程 ready 才回 200，给足时间
	resp, err := httpx.LocalClient(20 * time.Second).Do(req)
	if err != nil {
		fmt.Printf("· 交接请求发不出去（%v），退回 stop+start\n", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Printf("· 交接被拒（HTTP %d: %s），退回 stop+start\n", resp.StatusCode, b)
		return false
	}
	// 200 = 新进程已接上 socket、pid/lock 已改写。这里只确认它完全就位。
	for k := 0; k < 100; k++ { // 最多 5s
		if j := daemon.Running(); j != nil && j.PID != i.PID && pingProxy(j.Port) {
			fmt.Printf("✓ 代理已优雅重启  pid %d → %d  127.0.0.1:%d\n", i.PID, j.PID, j.Port)
			fmt.Println("  socket 无缝交接：在途请求由旧进程排空，会话不中断；接管状态未动。")
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("⚠ 交接已发出，但新进程 5 秒内没就位——看日志 %s\n", paths.LogFile())
	return false
}

// handoffSupported 问 daemon 的 status 端点：支持优雅交接吗。
func handoffSupported(port int) (bool, error) {
	resp, err := httpx.LocalClient(3 * time.Second).
		Get(fmt.Sprintf("http://127.0.0.1:%d/__newgate/status", port))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Handoff bool `json:"handoff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Handoff, nil
}

// cmdReload 显式触发重载。平时不需要——watcher 会自动发现。
func cmdReload() int {
	i := daemon.Running()
	if i == nil {
		fmt.Println("代理没在跑，配置会在下次启动时读取")
		return 0
	}
	if err := syscall.Kill(i.PID, syscall.SIGHUP); err != nil {
		return die(70, "发 SIGHUP 失败: "+err.Error())
	}
	fmt.Printf("✓ 已通知代理 (pid %d) 重读配置\n", i.PID)
	fmt.Println("  （平时不需要这个命令：配置改动会在 1 秒内自动生效）")
	return 0
}

// activeProblems 只检查**实际会被用到**的 provider，别因为某个用不到的
// profile 缺 key 就挡住启动。
func activeProblems(st *domain.State) []string {
	snap, err := store.Load()
	if err != nil {
		return []string{err.Error()}
	}
	used := map[string]bool{}
	check := func(profile string) {
		for _, p := range snap.Profiles {
			if p.Name != profile {
				continue
			}
			for _, cands := range p.Roles {
				for _, b := range cands {
					used[b.Provider] = true
				}
			}
		}
	}
	check(st.DefaultProfile)
	for _, p := range st.Active {
		check(p)
	}
	var out []string
	for name := range used {
		prov, ok := snap.Providers.Providers[name]
		if !ok {
			out = append(out, fmt.Sprintf("provider %q 未定义", name))
			continue
		}
		if prov.Key() == "" {
			hint := "在 providers.json 里填 api_key"
			if prov.APIKeyEnv != "" {
				hint = "设环境变量 " + prov.APIKeyEnv + " 或填 api_key"
			}
			out = append(out, fmt.Sprintf("provider %q 没有 key → %s", name, hint))
		}
	}
	return out
}

func pingProxy(port int) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d/__newgate/status", port))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond)
}

// notifyProxy 让运行中的代理立刻重读配置。
//
// 平时配置改动靠 watcher 的 1 秒轮询，对**手工编辑文件**足够快；
// 但 CLI 命令（--set-profile 等）是用户刚敲下的，必须立刻生效，
// 所以显式发 SIGHUP 强制重载。
func notifyProxy() {
	i := daemon.Running()
	if i == nil {
		return
	}
	_ = syscall.Kill(i.PID, syscall.SIGHUP)
}
