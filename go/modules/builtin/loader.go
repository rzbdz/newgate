// Package builtin 是 newgate 可执行程序的组合根。
//
// 各模块只声明自己的 capability、依赖和生命周期，不应知道“产品默认启用哪些
// 模块”。Loader 在这里列出默认集合，component.Manager 再根据 Need/Provide
// 计算真实顺序；列表顺序不是隐藏依赖。
//
// App 持有 Manager，并只向 main 暴露启动后真正需要的入口。替代产品或测试可
// 使用不同 Loader，而不修改任何业务模块。
package builtin

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/claudecode"
	"github.com/rzbdz/newgate/go/modules/claudecode_deepseek"
	"github.com/rzbdz/newgate/go/modules/claudecode_glm"
	"github.com/rzbdz/newgate/go/modules/cli"
	"github.com/rzbdz/newgate/go/modules/config"
	"github.com/rzbdz/newgate/go/modules/confighook"
	"github.com/rzbdz/newgate/go/modules/deepseek"
	gatewaycomponent "github.com/rzbdz/newgate/go/modules/gateway"
	"github.com/rzbdz/newgate/go/modules/glm"
	"github.com/rzbdz/newgate/go/modules/opencode"
	"github.com/rzbdz/newgate/go/modules/opencodeomo"
	runtimecomponent "github.com/rzbdz/newgate/go/modules/runtime"
	"github.com/rzbdz/newgate/go/modules/thinking"
)

// Loader 是默认产品装配清单。它只列出有哪些组件，
// 具体启动顺序仍由 capability 依赖图计算，而不是依赖这里的手工排列。
type Loader struct{}

// Load 返回内置组件集合；第三方组合根可以替换这个 Loader，而无需修改框架。
func (Loader) Load() ([]modules.Component, error) {
	return []modules.Component{
		config.New(),
		gatewaycomponent.New(),
		confighook.New(),
		runtimecomponent.New(),
		thinking.New(),
		claudecode.New(),
		deepseek.New(),
		glm.New(),
		claudecode_deepseek.New(),
		claudecode_glm.New(),
		opencode.New(),
		opencodeomo.New(),
		cli.New(),
	}, nil
}
