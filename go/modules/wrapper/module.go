// Package wrapper 拥有「PATH shim 接管」这条通路的策略。
//
// 为什么它是一个模块而不是 cmd 里的几行代码
//
// 被接管后的行为是：用户敲 `claude` → 内核 exec 的是 PATH 里那个**名叫
// claude 的符号链接**（指向 newgate）→ 我们的进程醒来，argv[0] 是 `claude`。
// 这件事有两半：
//
//	装链接：newgate on claude 时把符号链接建出去（runtime/injection 的字节操作）
//	解析：  进程醒来后认出「我是替 claude 跑的那一份」，注入 env 再 exec 真实
//	        的 claude（原来是 cmd/newgate 里的 argv0 判断）
//
// 两半分开的代价是静默断链：链接装了但没人解析 argv0，症状是 `claude` 启动了
// 但完全没走 newgate——用户以为接管生效了。所以它们同属一个模块，模块的名字
// 就是这件事的名字。
//
// 依赖方向：wrapper → runtime（要 Launch）、wrapper → confighook（要按名字查
// Agent 描述符）。反过来不成立——runtime 不知道 shim 的存在，它只负责「给一个
// Agent，启动它」。
package wrapper

import (
	"context"
	"path/filepath"
	"strings"

	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate/go/modules/runtime"
	"github.com/rzbdz/newgate/go/modules/runtime/injection"
)

type service struct {
	agents  agentapi.AgentCatalog
	runtime runtimeapi.Runtime
}

var _ Wrapper = (*service)(nil)

// New 声明 shim 接管组件。它不提供任何跨模块扩展点——外部只需要「分发一次
// 调用」和「装/摘链接」，没有第三个模块往它里面注册东西。
func New() modules.Component {
	instance := &service{}
	return modules.Component{
		Name: "wrapper",
		Requires: []modules.Requirement{
			modules.Need(runtimeapi.Capability),
			modules.Need(agentapi.AgentCatalogCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Wrapper(instance)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			instance.agents = modules.MustGet(ctx, agentapi.AgentCatalogCapability)
			instance.runtime = modules.MustGet(ctx, runtimeapi.Capability)
			return nil
		},
	}
}

// Dispatch 见接口注释。
//
// argv[0] 取 basename 并剥掉 Windows 的 .exe：用户双击、shell 别名、npm 装的
// 兜底脚本都会改变路径形态，但 `.exe` 后缀两边是同一个意思。剥后缀是历史行为
// （npm 的 claude 兜底脚本会以 claude.exe 出现），不能去掉。
func (s *service) Dispatch(_ context.Context, argv []string) (int, bool) {
	if len(argv) == 0 {
		return 0, false
	}
	name := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
	agent, ok := s.agents.Get(name)
	if !ok {
		return 0, false
	}
	// profile 传空 = 动态模式（注入档位名而不是真实模型名），这是 argv0 分发
	// 的既定语义：用户没在命令行上钉 profile，就让代理按请求时的当前配置解析。
	return s.runtime.Launch(agent, argv[1:], ""), true
}

// Install/Uninstall/Installed/Foreign 把 shim 的字节操作转给 injection。
//
// 这四项刻意不做成 capability 端口：它们的消费者是 CLI 命令（newgate on/off/
// shim），走的是 package 级调用而不是组件间依赖。本模块在这里提供的是**归属**
// ——shim 是 wrapper 的东西，别处要动它得先问 wrapper，而不是各自去戳 injection
// 这个库。
func (*service) Install(agentID string) (string, error) { return injection.Install(agentID) }
func (*service) Uninstall(agentID string) error         { return injection.Uninstall(agentID) }
func (*service) Installed() []string                    { return injection.Installed() }
func (*service) Foreign() []string                      { return injection.Foreign() }
