// Package root 是**唯一的 built-in 组件**，也是内核唯一认识的那个模块。
//
// # 它为什么在 modules/ 之外
//
// `tools/genmodules` 扫 `modules/` 生成装配清单，装一个模块 = 把目录复制进去。
// root 不属于那一类：它**摘不掉**，而且摘不掉的理由是结构性的——它是「进程入口」
// 这件事的提供者，别的模块都在它的两端。所以它由组合根显式装入（见 app 的
// Loader），不进扫描清单，也就不会长得像「一个普通模块」。
//
// # 它自己什么业务都不做
//
// root 只做一件事：提供**入口申报账本**（entry.Registry）。谁想当进程入口就往里
// 申报（cli 申报「默认」、wrapper 申报「argv0 是某个被接管的 client」、将来的
// web-daemon 申报「配置里写着 entry=web」）。组合根只做一次 Resolve，它自己
// 不认识 cli、不认识 wrapper，也不认识将来那个 web。
//
// 为什么这件事必须落在**用户摘不掉**的东西上：没有任何入口申报时，进程要能说
// 一句人话然后退出；而如果账本住在一个可摘的模块里，用户摘三次就得到一个开不了
// 机的二进制。这是 root 存在**唯一**的理由——不是「内核需要一个总管家」。
//
// # 它不是「组合根」
//
// 组合根（app）负责装图、启动、把这次调用交给入口；root 是**图里的一个节点**，
// 与别的节点一样有依赖声明和生命周期，只是它的消费者是组合根自己。两者的区别
// 看判据很清楚：root 认得内核（component）与入口契约（component/entry），
// 一个业务模块都不认得。
package root

import (
	"context"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/component/entry"
)

// TypeBuiltin 是 root 的分类标签。词汇表归产品层（见 component.Type 的注释），
// 这里只声明取值：**装配矩阵测试按它划边界**——除了这一项，其余每一项都必须
// 能被摘掉、也必须能独立装载。
const TypeBuiltin = "builtin"

// New 声明 root 组件。
//
// Requires 是空的：root 不依赖任何模块，任何模块也可以不依赖它——入口是自愿
// 申报的。一个没人申报入口的图是**合法**的（比如只装了数据面、由别的进程驱动），
// 那时组合根报一句人话并退出，不是崩（见 entry.Registry.Resolve）。
func New() modules.Component {
	table := entry.NewTable()
	return modules.Component{
		Name: "root",
		Type: TypeBuiltin,
		Provides: []modules.Provision{
			modules.Provide(entry.Capability, entry.Registry(table)),
		},
		Start: func(context.Context, modules.Context) error { return nil },
	}
}
