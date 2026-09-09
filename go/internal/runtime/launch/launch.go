// Package launch 是「启动一个被接管的 agent」这件事的唯一实现。
//
// 它同时服务两个入口：
//
//   - argv0 分发：`claude`（PATH shim 指向 newgate 二进制）→ main 检测到
//     basename 是 agent，转到这里；
//   - CLI 包装启动：`newgate claude --profile=ds` → cli 解析出 agent 与
//     profile，也转到这里。
//
// 它做的事（docs/16 §3.4）：解析真实可执行文件、必要时懒起代理、往子进程
// 注入 env（含真实模型名）、exec。除此之外不碰任何文件、不发任何网络请求。
//
// 槽位命名是这一层的核心决策（2026-09-09 定稿，见 buildInject 注释）：
// 默认注入**档位名**（heavy/mid/…）——会话发档位名回来，代理按请求时的
// 当前配置解析，`newgate --set-profile` 对跑着的会话立刻生效（动态）；
// 显式 --profile 钉死本次调用时才注入**真实模型名**（glm-5.3），显示的
// 就是这次真用的（钉死才配真名）。窗口声明（CLAUDE_CODE_*）两种模式
// 都注入，与命名无关。
package launch

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/platform/httpx"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"github.com/rzbdz/newgate/go/internal/store"
)

// Options 一次启动的覆盖项。零值 = 用 agent 的默认 profile。
type Options struct {
	// Profile 本次调用的 profile 覆盖（不写全局状态）。空 = 走 agent 默认。
	Profile string
}

// Launch 注入 env 并 exec 真实 agent。成功时进程被替换、不再返回；
// 返回的 int 是进程退出码（仅在失败路径返回）。
func Launch(a *agents.Agent, args []string, o Options) int {
	depth, _ := strconv.Atoi(os.Getenv("NEWGATE_DEPTH"))
	if depth >= 2 || os.Getenv("NEWGATE_DISABLE") == "1" {
		return execReal(a, args, nil) // 完全透传，不注入
	}

	st := store.LoadState()
	if _, err := a.FindReal(injection.Dir()); err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		return 69
	}

	// 代理懒启动；否则 agent 一进来就 connection refused。
	// （EnsureControlToken：别的用户停这个懒起的 daemon 也得有令牌可用）
	if daemon.Running() == nil {
		_ = store.EnsureControlToken()
		fmt.Fprintf(os.Stderr, "newgate: proxy not running, starting…\n")
		if _, err := daemon.Spawn(st.Port); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: failed to start proxy: %v\n"+
				"  (NEWGATE_DISABLE=1 bypasses newgate entirely)\n", err)
			return 69
		}
		waitProxy(st.Port)
	}

	// 解析本次的链头：命令行 --profile > NEWGATE_PROFILE/PRESET > agent 默认。
	explicit := o.Profile
	if explicit == "" {
		explicit = firstNonEmpty(os.Getenv("NEWGATE_PROFILE"), os.Getenv("NEWGATE_PRESET"))
	}
	active := explicit
	if active == "" {
		active = st.ActiveFor(a.ID)
	}

	inject := buildInject(a, st, active, explicit)
	inject["NEWGATE_DEPTH"] = strconv.Itoa(depth + 1)
	return execReal(a, args, inject)
}

// buildInject 算出要叠加给子进程的整组 env（NEWGATE_DEPTH 除外）。
// 单独成函数是为了可测：只依赖读盘快照和入参，不 exec、不碰 daemon。
//
// 槽位命名有两种模式（2026-09-09 定稿）：
//
//   - 动态（默认）：槽位保持**档位名**（heavy/mid/…）。会话把档位名发
//     回来，代理按**请求时**的当前配置解析——`newgate --set-profile`
//     对跑着的会话立刻生效。界面显示的是语义层名字，实际模型看
//     X-Newgate-Route / `newgate tier`，两边各自为真、互不冒充。
//   - 钉死（显式 --profile / NEWGATE_PROFILE / argv0 分发）：注入**真实
//     模型名**。本次调用已被用户钉在这个 profile 上，显示的就是这次
//     真用的——钉死才配得上真名。
//
// 窗口声明两条模式都注入：CLAUDE_CODE_MAX_CONTEXT_TOKENS 走客户端的
// 「未知模型」分支（档位名和真实名对它都是未知，效果相同），
// AUTO_COMPACT_WINDOW 无条件优先级最高——窗口修复不依赖命名模式。
func buildInject(a *agents.Agent, st *domain.State, active, explicit string) map[string]string {
	inject := a.BuildEnv(st.Port, "newgate-local") // 槽位默认 = 档位名（动态模式）

	if snap, err := store.Load(); err == nil {
		// 钉死模式：槽位换成该 profile 链头的真实模型名。解析不出就保留
		// 档位名（fail-open，docs/16 §6.1）：代理仍能按档位路由。
		if explicit != "" {
			for _, s := range a.EnvSlots() {
				if b, ok := resolve.PrimaryBinding(s.Tier, snap.Profiles, snap.Providers, active); ok {
					inject[s.EnvVar] = b.Model
				}
			}
		}

		// 主力模型的真实窗口（profile 里声明）。不给 Claude Code 声明的
		// 话，它对不认识的模型名（档位名或真实名都一样）按 200k 窗口假设
		// 提前 compact（2026-09 实测）。两个 env 都只在配置 >0 时注入；
		// 其他 agent（如 opencode）不认识这些变量，忽略之，无害。
		for _, p := range snap.Profiles {
			if p.Name != active {
				continue
			}
			if p.ContextWindow > 0 {
				inject["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(p.ContextWindow)
			}
			if p.AutoCompactWindow > 0 {
				inject["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = strconv.Itoa(p.AutoCompactWindow)
			}
			break
		}
	}

	// per-invocation 覆盖：把 profile 编码进 base URL 路径（/p/<profile>）。
	// 代理从路径读出覆盖，完全不改全局状态（docs/16 §3.4）。
	if explicit != "" && a.BaseURLEnv != "" {
		inject[a.BaseURLEnv] = fmt.Sprintf("http://127.0.0.1:%d/a/%s/p/%s", st.Port, a.ID, active)
	}
	return inject
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// execReal 找到真实可执行文件并用注入后的 env 覆盖进程。
func execReal(a *agents.Agent, args []string, inject map[string]string) int {
	real, err := a.FindReal(injection.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		return 69
	}

	// 受控的 env 白名单：继承 → 丢掉干扰项 → 叠加我们的。
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

	execArgs := append([]string{real}, args...)
	if err := syscall.Exec(real, execArgs, env); err != nil {
		// 没有 shebang 的脚本（例如 npm 的 claude.exe 兜底脚本、或 CRLF
		// 行尾损坏的 shebang）会让内核返回 ENOEXEC（exec format error）。
		// 交给 /bin/sh 执行，让它打印脚本自己那清晰明了的错误信息，
		// 而不是给用户一个神秘的 "exec format error"。
		if errors.Is(err, syscall.ENOEXEC) {
			shArgs := append([]string{"/bin/sh", real}, args...)
			if err2 := syscall.Exec("/bin/sh", shArgs, env); err2 != nil {
				fmt.Fprintf(os.Stderr, "newgate: 经 /bin/sh 执行 %s 也失败: %v\n", real, err2)
				return 70
			}
			return 0 // 不会走到这里：Exec 成功则不返回
		}
		fmt.Fprintf(os.Stderr, "newgate: exec %s failed: %v\n", real, err)
		return 70
	}
	return 0 // 不会走到这里：Exec 成功则不返回
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// waitProxy 轮询代理直到它响应，或约 3s 后放弃。
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
