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

import modules "github.com/rzbdz/newgate/go/component"

//go:generate go run github.com/rzbdz/newgate/go/tools/genmodules

// Loader 是默认产品装配清单：直接采用构建期扫描的结果。
// 具体启动顺序仍由 capability 依赖图计算，而不是依赖生成顺序。
type Loader struct{}

// Load 返回扫描得到的组件集合；第三方组合根可以替换这个 Loader，
// 而无需修改框架。
func (Loader) Load() ([]modules.Component, error) {
	return generatedComponents(), nil
}
