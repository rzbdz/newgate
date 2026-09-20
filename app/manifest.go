package app

import (
	"fmt"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// Entry 是一个组件在**装配清单里的身份**：目录名 + 组件定义。
//
// 为什么身份是目录名而不是组件名：目录名是「装哪个 / 关哪个」的键。发行版规格书
// 里 disable 写的就是目录名（发行版有 `claudecode_deepseek`），而组件名不必与它相同
// （`claudecode-deepseek`）。按组件名去关会**静默关不掉**（名字没对上，谁也不会
// 报错），所以键从扫描来（tools/genmodules 扫的就是目录名），这里原样带着。
// 内核这一侧今天恰好个个同名——巧合，不是规矩。
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

// Selection 是**由消费者决定**的一组组件：内核自带的减去关掉的、加上 Extra。
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
	// AllCore 关掉**内核自带的全部可关模块**——「这个发行版只要框架 + 我自己的模块」。
	//
	// 为什么是一个字段，而不是让 Disable 把目录名列满：列满的写法有两份代价，而且
	// 都是静默的。其一是**名单会腐坏**——它与 CoreModules() 是两个副本，内核加一个
	// 模块时那份名单不会跟着长，于是「我以为全关掉了」的发行版悄悄多装了一个模块。
	// 其二是它把一句话能说清的产品决定写成了十几行数据（骨架发行版就是这样）。
	// AllCore 表达的是「内核自带的都不要」，它跟着 CoreModules() 一起长。
	//
	// 「可关的」是关键词：组合根自己要用的那个端口（入口账本）**谁提供谁就关不掉**，
	// AllCore 也关不掉它——否则一个发行版能把自己变成一个没人能回答「这次调用归谁」
	// 的二进制。这不是例外，那就是「可关」的定义边界（见 compositionRootPorts）。
	//
	// 与 Disable 同时给会报错：那两个说法是同一件事（那些名字本来就在 AllCore 里）。
	AllCore bool
	// Disable 是要关掉的内核模块，写**目录名**。名字必须点得中——点不中就报错。
	//
	// 要关掉全部请用 AllCore（规格书里写 `"disable": ["*"]`，见 tools/distgen）。
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
	known := make(map[string]Entry, len(core))
	for _, e := range core {
		known[e.Dir] = e
	}
	extra := make(map[string]bool, len(s.Extra))
	for _, e := range s.Extra {
		if e.Dir == "" {
			return nil, fmt.Errorf("装配选择：Extra 里有一个没写目录名的组件（组件名 %q）——"+
				"目录名是关掉它、以及报错时说清是谁的凭据", e.Component.Name)
		}
		extra[e.Dir] = true
	}

	off := make(map[string]bool, len(core))
	if s.AllCore {
		// 两个说法说同一件事时必须报错，不能取并集：那样 `AllCore` 与一长串
		// Disable 会同时存在，而后者是前者的过时副本——正是这个字段要消掉的东西。
		if len(s.Disable) > 0 {
			return nil, fmt.Errorf("装配选择：AllCore 与 Disable 同时给了（%v）——"+
				"前者已经包含后者。两个都要就只留 AllCore；只想关掉其中几个就别写 AllCore", s.Disable)
		}
		for _, e := range core {
			if _, serves := servesCompositionRoot(e.Component); serves {
				continue // 关不掉的那个不在「可关的」里面
			}
			off[e.Dir] = true
		}
	}
	for _, dir := range s.Disable {
		e, isCore := known[dir]
		if !isCore {
			if extra[dir] {
				return nil, fmt.Errorf("装配选择：disable 点名了 %q，但那是 Extra 里的模块——"+
					"Extra 是你自己交上来的表，不想装就别把它列进去"+
					"（disable 只用来关掉内核自带的模块；列在这里是**没有效果**的，而你多半以为它关掉了）", dir)
			}
			return nil, fmt.Errorf("装配选择：disable 点名了 %q，但内核与自己都没有这个模块"+
				"（写的是**目录名**吗？比如 claudecode_deepseek 而不是 claudecode-deepseek；"+
				"要关掉全部请用 AllCore）", dir)
		}
		if port, serves := servesCompositionRoot(e.Component); serves {
			return nil, fmt.Errorf("装配选择：disable 点名了 %q（组件 %s），但它提供了组合根自己要用的端口 %q——"+
				"关掉它，进程就没有任何东西能回答「这次调用归谁」了。"+
				"它摘不掉这件事不是一张名单说了算，而是组合根自己的依赖说了算（见 compositionRootPorts）",
				dir, e.Component.Name, port)
		}
		off[dir] = true
	}

	// 顺序不是依赖声明：真实启动顺序由 capability 依赖图在构图期算（见 package 注释）。
	out := make([]modules.Component, 0, len(core)+len(s.Extra))
	owner := make(map[string]string, len(core)+len(s.Extra))
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
	if len(out) == 0 {
		return nil, fmt.Errorf("装配选择：一个组件都没有" +
			"（disable 把内核模块全关掉了？）——空图跑起来只会让人以为命令坏了")
	}
	return out, nil
}

// compositionRootPorts 是**组合根自己要用的端口**：它起完图之后必须拿到的那些。
//
// 今天只有一个——入口申报账本（app.Main 起完图第一件事就是问它「这次调用归谁」）。
// 这不是一张「不可摘模块的名单」，而是本包自己的依赖清单：谁提供这些端口，谁就
// 摘不掉（见 Load），因为摘掉它等于组合根拿不到自己活着需要的东西。
//
// 边界必须在**这个**方向：机制不认模块名。2026-09-20 之前这里写的是
// `case dir == "root"`——「哪个模块不可摘」于是被硬编码进内核一次、又被模块自己
// 用 `Type = builtin` 声明了一次，两处说的是同一件事却各说各的，改一处不会让
// 另一处红。现在只有一个说法（组合根的依赖），Type 那个标记只剩摘除矩阵在用。
//
// 它也必须**短**：这个列表每长一条，模块的可替换性就少一分。能不加就不加。
func compositionRootPorts() []string {
	return []string{modules.Name(entry.Capability)}
}

// servesCompositionRoot 报告组件是否提供了组合根自己要用的端口；是的话连同
// 端口名一起返回（报错要用它点名「缺的是哪东西」，而不是「哪个模块」）。
func servesCompositionRoot(c modules.Component) (string, bool) {
	for _, provision := range c.Provides {
		for _, port := range compositionRootPorts() {
			if provision.Name() == port {
				return port, true
			}
		}
	}
	return "", false
}
