package cli

import (
	"fmt"
	"log"
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
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
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

	if err := daemon.AcquireLock(); err != nil {
		lg.Printf("抢锁失败: %v", err)
		return 69
	}
	defer daemon.RemoveLock()

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
			lg.Printf("收到 %v，退出", s)
			srv.Shutdown()
			daemon.RemoveLock()
			daemon.RemovePid()
			os.Exit(0)
		}
	}()

	lg.Printf("newgate %s (构建于 %s) 启动，默认 profile=%s，配置热更新已开启",
		Version, pretty(BuildTime), watcher.Current().State.DefaultProfile)
	if err := srv.Start(); err != nil {
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

	st := store.LoadState()
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

	reps, err := injection.ApplyAll(st.Port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
	}
	for _, r := range reps {
		if r.Skipped != "" {
			fmt.Printf("- %s  (跳过: %s)\n", r.File, r.Skipped)
			continue
		}
		fmt.Printf("✓ 接管 %s\n", r.File)
		for i, rw := range r.Rewrites {
			if i < 4 {
				fmt.Printf("    %s\n", rw)
			}
		}
		if len(r.Rewrites) > 4 {
			fmt.Printf("    … 共 %d 处改写\n", len(r.Rewrites))
		}
		if r.Fuzzy > 0 {
			fmt.Printf("    ⚠ 其中 %d 处没命中精确规则，归到了中档，建议核对\n", r.Fuzzy)
		}
	}
	st.TakenOver = true
	_ = store.SaveState(st)
	fmt.Printf("\n配置改动会自动热更新，不需要重启代理。\n")
	fmt.Printf("原配置备份在 %s/original/\n", paths.BackupDir())
	return 0
}

func cmdStop() int {
	restored, err := injection.RestoreAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
	}
	for _, f := range restored {
		fmt.Printf("✓ 已还原 %s\n", f)
	}
	pid, err := daemon.Stop()
	if err != nil {
		return die(70, err.Error())
	}
	if pid > 0 {
		fmt.Printf("✓ 代理已停 (pid %d)\n", pid)
	} else {
		fmt.Println("代理本来没在跑")
	}
	st := store.LoadState()
	st.TakenOver = false
	_ = store.SaveState(st)
	return 0
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
