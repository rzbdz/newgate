package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rzbdz/newgate/go/internal/config"
	"github.com/rzbdz/newgate/go/internal/daemon"
	"github.com/rzbdz/newgate/go/internal/shim"
	"github.com/rzbdz/newgate/go/internal/tools"
)

// runAsWrapper 当 argv[0] 是某个工具名时（PATH shim），注入 env 后 exec 真正的工具。
//
// 为什么用 PATH shim 而不是写工具的配置文件：用户的 shell rc 里往往已经
// export 了 ANTHROPIC_BASE_URL / AUTH_TOKEN，而 shell 环境优先级高于
// settings.json 的 env 块——写配置文件会被静默盖掉。shim 在子进程里显式
// 设置，能盖过一切，而且不需要改用户 rc 里已有的任何一行。
func runAsWrapper(t *tools.Tool, argv []string) {
	// 递归保护：shim 找真实工具时会跳过自己的目录，但万一还是绕回来了
	depth := 0
	if v := os.Getenv("NEWGATE_DEPTH"); v != "" {
		fmt.Sscanf(v, "%d", &depth)
	}
	if depth >= 2 || os.Getenv("NEWGATE_DISABLE") == "1" {
		execReal(t, argv, nil) // 完全透传，不注入
		return
	}

	st := config.LoadState()
	if _, err := t.FindReal(shim.Dir()); err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		os.Exit(69)
	}

	// 代理没在跑就懒启动——否则工具会拿到 connection refused
	if daemon.Running() == nil {
		fmt.Fprintf(os.Stderr, "newgate: 代理未运行，正在启动…\n")
		if _, err := daemon.Spawn(st.Port); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: 启动代理失败: %v\n（用 NEWGATE_DISABLE=1 可绕过 newgate）\n", err)
			os.Exit(69)
		}
		waitProxy(st.Port)
	}

	inject := t.BuildEnv(st.Port, "newgate-local")
	inject["NEWGATE_DEPTH"] = fmt.Sprint(depth + 1)
	// per-invocation 覆盖：newgate-<profile> 前缀式调用（见 docs/16 §3）
	if p := os.Getenv("NEWGATE_PRESET"); p != "" {
		inject["NEWGATE_PRESET"] = p
	}
	execReal(t, argv, inject)
}

func execReal(t *tools.Tool, argv []string, inject map[string]string) {
	real, err := t.FindReal(shim.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		os.Exit(69)
	}

	// 从受控白名单构造子进程 env：继承 → 删干扰变量 → 叠加注入
	drop := map[string]bool{}
	for _, k := range t.UnsetEnv {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
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

	// exec 而不是 fork：进程直接被替换，信号/退出码/tty 全部天然正确
	args := append([]string{real}, argv[1:]...)
	if err := syscall.Exec(real, args, env); err != nil {
		fmt.Fprintf(os.Stderr, "newgate: exec %s 失败: %v\n", real, err)
		os.Exit(70)
	}
}

// wrapperName argv[0] 的 basename 去掉可能的 .exe。
func wrapperName(argv0 string) string {
	return strings.TrimSuffix(filepath.Base(argv0), ".exe")
}
