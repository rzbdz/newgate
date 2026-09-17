package wrapper_test

import (
	"context"
	"reflect"
	"testing"

	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime"
	wrapperapi "github.com/rzbdz/newgate/go/modules/wrapper"
	"github.com/rzbdz/newgate/go/testing/testkit"
)

// launchCall 记录一次 Runtime.Launch 的入参。
type launchCall struct {
	agent   string
	args    []string
	profile string
}

// fakeRuntime 是 Runtime 端口的桩：只记录调用，不 exec 任何东西。
// 真实的 Launch 会 syscall.Exec 掉整个测试进程，所以这里必须换成桩。
type fakeRuntime struct{ calls []launchCall }

func (f *fakeRuntime) Launch(agent *agentapi.Agent, args []string, profile string) int {
	id := ""
	if agent != nil {
		id = agent.ID
	}
	f.calls = append(f.calls, launchCall{agent: id, args: args, profile: profile})
	return 0
}

var _ runtimeapi.Runtime = (*fakeRuntime)(nil)

// harness 装出「wrapper + 它的两个依赖」这张最小图。
//
// 这正是 testkit 存在的理由：被测模块不必知道依赖从哪来，只要图上有人提供
// 那两个端口就能启动。这里用桩替掉 runtime（真 Launch 会 exec 掉进程），
// 但 wrapper 模块本身是**真的**——它的 Requires/Provides/Start 全走真实路径。
func harness(t *testing.T, agents ...*agentapi.Agent) (wrapperapi.Wrapper, *fakeRuntime) {
	t.Helper()
	runtime := &fakeRuntime{}
	catalog := testkit.NewCatalog().Add(agents...)
	graph := testkit.Start(t,
		modules.Component{
			Name:     "stub-runtime",
			Provides: []modules.Provision{modules.Provide(runtimeapi.Capability, runtimeapi.Runtime(runtime))},
		},
		modules.Component{
			Name:     "stub-catalog",
			Provides: []modules.Provision{modules.Provide(agentapi.AgentCatalogCapability, catalog.AsCatalog())},
		},
		wrapperapi.New(),
	)
	// 依赖顺序：wrapper 必须在两个提供者之后启动，否则 Start 里的 MustGet 会 panic。
	graph.Before("stub-runtime", "wrapper")
	graph.Before("stub-catalog", "wrapper")
	return testkit.Get(graph, wrapperapi.Capability), runtime
}

func TestDispatchClaimsAgentInvocations(t *testing.T) {
	wrapper, runtime := harness(t,
		&agentapi.Agent{ID: "claude"},
		&agentapi.Agent{ID: "opencode"},
	)

	tests := []struct {
		name     string
		argv     []string
		claimed  bool
		wantID   string
		wantArgs []string
	}{
		{
			name:     "PATH shim 转过来的绝对路径",
			argv:     []string{"/home/u/.config/newgate/bin/claude", "--resume", "abc"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{"--resume", "abc"},
		},
		{
			name:     "argv0 就是裸名字",
			argv:     []string{"opencode"},
			claimed:  true,
			wantID:   "opencode",
			wantArgs: []string{},
		},
		{
			// npm 装的兜底脚本会以 claude.exe 出现，剥后缀是历史行为。
			// 用 POSIX 路径而不是 `C:\bin\claude.exe`：反斜杠只在 Windows 上是
			// 分隔符，在 Linux 上 filepath.Base 会把整串当文件名——那样测的是
			// Windows 语义，在 Linux 上必然失败，属于测试写错而不是代码写错。
			name:     "Windows 风格 .exe 后缀要剥掉",
			argv:     []string{"/usr/local/bin/claude.exe", "hi"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{"hi"},
		},
		{
			name:     "裸的 .exe 名字也要认",
			argv:     []string{"claude.exe"},
			claimed:  true,
			wantID:   "claude",
			wantArgs: []string{},
		},
		{
			name:    "控制 CLI 自己不该被认领",
			argv:    []string{"/usr/local/bin/newgate", "status"},
			claimed: false,
		},
		{
			name:    "不认识的命令名不该被认领",
			argv:    []string{"/usr/bin/ls", "-la"},
			claimed: false,
		},
		{
			// 空 argv 是内核理论上不会给但防御性要处理的情况（比如测试里手构造）。
			name:    "空 argv 不 panic",
			argv:    nil,
			claimed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(runtime.calls)
			_, claimed := wrapper.Dispatch(context.Background(), tt.argv)
			if claimed != tt.claimed {
				t.Fatalf("Dispatch(%v) claimed = %v, want %v", tt.argv, claimed, tt.claimed)
			}
			if !tt.claimed {
				if len(runtime.calls) != before {
					t.Fatalf("未被认领却调了 Launch: %v", runtime.calls[before:])
				}
				return
			}
			if len(runtime.calls) != before+1 {
				t.Fatalf("认领了但 Launch 调用次数 = %d, want %d", len(runtime.calls)-before, 1)
			}
			call := runtime.calls[before]
			if call.agent != tt.wantID {
				t.Errorf("Launch agent = %q, want %q", call.agent, tt.wantID)
			}
			if !reflect.DeepEqual(call.args, tt.wantArgs) {
				t.Errorf("Launch args = %v, want %v", call.args, tt.wantArgs)
			}
			// argv0 分发是**动态模式**：不钉 profile，让代理按请求时的当前配置解析。
			// 钉死了的话 newgate --set-profile 对跑着的会话就不生效了。
			if call.profile != "" {
				t.Errorf("Launch profile = %q, want 空（argv0 分发必须走动态模式）", call.profile)
			}
		})
	}
}

// TestDispatchStripsArgv0 锁住「argv0 不进透传参数」。
// 真实启动是 syscall.Exec(real, [real]+args, env)，多带一个 argv0 会让
// claude 把 `claude` 当成要打开的目录。
func TestDispatchStripsArgv0(t *testing.T) {
	wrapper, runtime := harness(t, &agentapi.Agent{ID: "claude"})
	wrapper.Dispatch(context.Background(), []string{"claude", "a", "b"})
	if len(runtime.calls) != 1 {
		t.Fatalf("Launch 调用次数 = %d, want 1", len(runtime.calls))
	}
	if got := runtime.calls[0].args; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("透传参数 = %v, want [a b]（argv0 不该出现）", got)
	}
}
