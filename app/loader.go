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
	modules "github.com/rzbdz/newgate/component"
)

//go:generate go run github.com/rzbdz/newgate/tools/genmodules

// Loader is the kernel's own assembly: **whatever the scan finds**, nothing more.
//
// It is empty on purpose. A composition root that wants a product passes an
// app.Selection (a distribution does); Loader{} is what `go test ./...` and the
// kernel's own binary see when nobody has chosen anything.
type Loader struct{}

// Load returns the component set; a third-party composition root can replace this
// Loader without touching the framework.
//
// "The graph is the scanned list" stays an equation with no exceptions: the entry
// ledger used to be installed explicitly by the composition root, which made it the
// one module the root knew by name. It is a normal scanned module now, and what
// makes it unremovable is not this file but the root's own dependency on its port
// (see compositionRootPorts in manifest.go).
func (Loader) Load() ([]modules.Component, error) {
	return generatedComponents(), nil
}
