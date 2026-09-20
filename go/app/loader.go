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
)

//go:generate go run github.com/rzbdz/newgate/go/tools/genmodules

// Loader 是默认产品装配清单：**就是扫描结果**，一个不多、一个不少。
type Loader struct{}

// Load 返回组件集合；第三方组合根可以替换这个 Loader，而无需修改框架。
//
// 这里没有"额外塞进来"的组件（2026-09-20 之前有一个：入口账本的拥有者由组合根
// 显式装入，因为当时它住在 modules/ 之外、不参与扫描）。现在它在 modules/ 里，
// 与别的模块一样被扫描——「图 = 扫描清单」于是成了一条**没有例外**的等式
// （见 graph_test.go 的 TestGraphCoversEveryModule）。
//
// 它摘不掉这件事与这里无关：那是 app.Selection 的判断，判据是「组合根自己要用
// 它提供的端口」（见 manifest.go），而不是「组合根额外装了它」。
func (Loader) Load() ([]modules.Component, error) {
	return generatedComponents(), nil
}
