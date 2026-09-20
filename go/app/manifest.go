package app

import (
	"fmt"

	modules "github.com/rzbdz/newgate/go/component"
)

// Entry 是一个组件在**装配清单里的身份**：目录名 + 组件定义。
//
// 为什么身份是目录名而不是组件名：目录名是「装哪个 / 关哪个」的键。发行版规格书
// 里 disable 写的就是目录名（`claudecode_deepseek`），而组件名是
// `claudecode-deepseek`——十四个模块里有三个两者不同。按组件名去关会**静默关不掉**
// （名字没对上，谁也不会报错），所以键从扫描来（tools/genmodules 扫的就是目录名），
// 这里原样带着。
type Entry struct {
	Dir       string
	Component modules.Component
}

// CoreModules 返回**内核自带的全部组件**，按目录名排序；实现落在构建期生成的
// modules_gen.go 里。
//
// 为什么要有这个出口：组合根的装配逻辑留在内核，但「装哪些」从 2026-09-20 起是
// **消费者的事**（发行版装它自己的模块，并按需关掉内核的几个）。消费者需要一张
// 带身份的表——只给一串裸组件就没法按目录名关，也没法在装配前校验名字拼错了。
//
// 实现必须是生成物：app 的非测试源码不许 import 任何业务模块（见 direction_test.go），
// 而这份清单列的就是全部模块名——modules_gen.go 是它唯一合法的住处。
func CoreModules() []Entry { return coreModules() }

// Selection 是**由消费者决定**的一组组件：内核自带的减去 Disable、加上 Extra。
//
// 发行版就是拿它把自己的模块交给内核的组合根：
//
//	app.Main(ctx, app.Options{Loader: app.Selection{
//	    Disable: []string{"cli"},                       // 关掉内核的界面
//	    Extra:   []app.Entry{{Dir: "simple-cli", Component: simplecli.New()}},
//	}})
//
// 内核侧零改动：组合根仍然不认识任何模块，它只认识这张表。
type Selection struct {
	// Disable 是要关掉的内核模块，写**目录名**。名字必须点得中——点不中就报错。
	Disable []string
	// Extra 是消费者自己的模块，形态与内核模块完全一致（目录名 + New()）。
	Extra []Entry
}

// Load 实现 modules.Loader。
//
// 这里每一条拒绝都是响亮的，理由是同一条：**这一层的错全会伪装成别的问题**。
// 一个拼错的 disable 名字看起来和「关掉了」一模一样（实际是「我以为关了，它还在
// 跑」）；一次重复装配要到构图期才以一句 `duplicate component` 出现，那时离
// 「我在清单里多写了一份」已经很远了。
func (s Selection) Load() ([]modules.Component, error) {
	core := CoreModules()
	known := make(map[string]bool, len(core))
	for _, e := range core {
		known[e.Dir] = true
	}
	for _, e := range s.Extra {
		if e.Dir == "" {
			return nil, fmt.Errorf("装配选择：Extra 里有一个没写目录名的组件（组件名 %q）——"+
				"目录名是关掉它、以及报错时说清是谁的凭据", e.Component.Name)
		}
		known[e.Dir] = true
	}

	off := make(map[string]bool, len(s.Disable))
	for _, dir := range s.Disable {
		switch {
		case dir == "root":
			return nil, fmt.Errorf("装配选择：disable 点了 root 的名。它是唯一不可摘的 built-in" +
				"（入口账本住在它身上），关掉它没有任何入口能回答「这次调用归谁」")
		case !known[dir]:
			return nil, fmt.Errorf("装配选择：disable 点名了 %q，但内核与自己都没有这个模块"+
				"（写的是**目录名**吗？比如 claudecode_deepseek 而不是 claudecode-deepseek）", dir)
		}
		off[dir] = true
	}

	// built-in 永远在，且排在前面：它提供入口账本，别的模块在 Start 里往它申报。
	builtins := builtinComponents()
	out := append([]modules.Component{}, builtins...)
	owner := make(map[string]string, len(core)+len(s.Extra)+len(builtins))
	for _, c := range builtins {
		owner[c.Name] = "built-in"
	}
	add := func(dir string, c modules.Component) error {
		if prev, dup := owner[c.Name]; dup {
			return fmt.Errorf("装配选择：组件 %q 装了两次（%s 与 %s）——"+
				"同名的两个组件会让这次装配在构图期才失败", c.Name, prev, dir)
		}
		owner[c.Name] = dir
		out = append(out, c)
		return nil
	}
	for _, e := range core {
		if off[e.Dir] {
			continue
		}
		if err := add(e.Dir, e.Component); err != nil {
			return nil, err
		}
	}
	for _, e := range s.Extra {
		if err := add(e.Dir, e.Component); err != nil {
			return nil, err
		}
	}
	if len(out) == len(builtins) {
		return nil, fmt.Errorf("装配选择：除了 built-in 一个组件都没有" +
			"（disable 把内核模块全关掉了？）——空图跑起来只会让人以为命令坏了")
	}
	return out, nil
}
