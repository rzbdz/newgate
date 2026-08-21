package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/platform/httpx"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"github.com/rzbdz/newgate/go/internal/store"
)

// runWrapper injects env and execs the real agent.
//
// This is the argv0-dispatch path: the shim symlink /config/newgate/bin/claude
// points at this binary, so when a user types `claude`, we land here, resolve
// the real claude elsewhere on PATH, set the env, and exec it.
func runWrapper(a *agents.Agent, argv []string) {
	depth, _ := strconv.Atoi(os.Getenv("NEWGATE_DEPTH"))
	if depth >= 2 || os.Getenv("NEWGATE_DISABLE") == "1" {
		execReal(a, argv, nil) // fully transparent, no injection
		return
	}

	st := store.LoadState()
	if _, err := a.FindReal(injection.Dir()); err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		os.Exit(69)
	}

	// Start the proxy lazily; otherwise the agent gets a connection refused.
	if daemon.Running() == nil {
		fmt.Fprintf(os.Stderr, "newgate: proxy not running, starting…\n")
		if _, err := daemon.Spawn(st.Port); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: failed to start proxy: %v\n"+
				"  (NEWGATE_DISABLE=1 bypasses newgate entirely)\n", err)
			os.Exit(69)
		}
		waitProxy(st.Port)
	}

	inject := a.BuildEnv(st.Port, "newgate-local")
	inject["NEWGATE_DEPTH"] = strconv.Itoa(depth + 1)
	// per-invocation profile override: /p/<profile> is read from the URL path.
	if p := os.Getenv("NEWGATE_PRESET"); p != "" {
		// TODO(M1): argv0 preset dispatch (newgate-toy claude) — docs/16 §3.
		inject["NEWGATE_PRESET"] = p
	}
	execReal(a, argv, inject)
}

func execReal(a *agents.Agent, argv []string, inject map[string]string) {
	real, err := a.FindReal(injection.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		os.Exit(69)
	}

	// Controlled allow-list env: inherit → drop interference → layer ours.
	drop := map[string]bool{}
	for _, k := range a.UnsetEnv {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		k := kv
		if i := indexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if drop[k] {
			continue
		}
		if _, overridden := inject[k]; overridden {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range inject {
		env = append(env, k+"="+v)
	}

	args := append([]string{real}, argv[1:]...)
	if err := syscall.Exec(real, args, env); err != nil {
		// 没有 shebang 的脚本（例如 npm 的 claude.exe 兜底脚本、或 CRLF
		// 行尾损坏的 shebang）会让内核返回 ENOEXEC（exec format error）。
		// 交给 /bin/sh 执行，让它打印脚本自己那清晰明了的错误信息，
		// 而不是给用户一个神秘的 "exec format error"。
		if errors.Is(err, syscall.ENOEXEC) {
			shArgs := append([]string{"/bin/sh", real}, argv[1:]...)
			if err2 := syscall.Exec("/bin/sh", shArgs, env); err2 != nil {
				fmt.Fprintf(os.Stderr, "newgate: 经 /bin/sh 执行 %s 也失败: %v\n", real, err2)
				os.Exit(70)
			}
			return // 不会走到这里：Exec 成功则不返回
		}
		fmt.Fprintf(os.Stderr, "newgate: exec %s failed: %v\n", real, err)
		os.Exit(70)
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// waitProxy polls the proxy until it responds, or gives up after ~3s.
func waitProxy(port int) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if tcpAlive(port) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func tcpAlive(port int) bool { return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond) }
