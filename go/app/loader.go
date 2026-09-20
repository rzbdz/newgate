// Package app 是 newgate 可执行程序的组合根。
//
// 各模块只声明自己的 capability、依赖和生命周期，不应知道"产品默认启用哪些
// 模块"。装配清单由 tools/genmodules 在构建期扫描 modules/ 生成（见
// modules_gen.go），component.Manager 再根据 Need/Provide 计算真实启动顺序；
// 清单顺序不是隐藏依赖。
//
// **装一个模块 = 把目录复制进 modules/、重新编译**，不需要改任何清单。
// 判据见 tools/genmodules：目录含根 module.go 且导出 `func New() modules.Component`。
//
// App 持有 Manager，并只向 main 暴露启动后真正需要的入口。替代产品或测试可
// 使用不同 Loader，而不修改任何业务模块。
package app

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/root"
)

//go:generate go run github.com/rzbdz/newgate/go/tools/genmodules

// Loader 是默认产品装配清单：扫描结果 + **唯一那个 built-in**。
// 具体启动顺序仍由 capability 依赖图计算，而不是依赖声明顺序。
type Loader struct{}

// Load 返回组件集合；第三方组合根可以替换这个 Loader，而无需修改框架。
//
// root 是**唯一被显式装入**的组件（见 go/root）：它不参与 modules/ 的扫描，
// 因为它摘不掉——入口账本住在它身上，没有任何入口申报时进程要说人话地退出，
// 而一个能被用户摘掉的入口账本等于让三次操作换来一个开不了机的二进制。
func (Loader) Load() ([]modules.Component, error) {
	return append(builtinComponents(), generatedComponents()...), nil
}

// builtinComponents 是**摘不掉**的那一组。今天只有 root 一个，判据是结构性的：
// 它是进程入口账本的提供者，没有它就没人能回答「这次调用归谁」。
//
// 为什么单独一个函数而不是内联一句：装配测试要拿它算「图 = 扫描 + built-in」
// 这条等式（见 graph_test.go 的 TestGraphCoversEveryModule），而那条断言正是
// 「没有第二个模块绕过扫描被偷偷装进来」的守卫。
func builtinComponents() []modules.Component {
	return []modules.Component{root.New()}
}
