package component

import (
	"sort"

	"github.com/rzbdz/newgate/lib/i18n"
)

// Plan 是一次装配的**图纸**：排好序的组件、它们各自要什么、谁提供什么。
//
// 它不含生命周期——一个 Start 都不跑，没有端口被占、没有文件被写、没有全局
// 注册表被改。所以它可以被反复求（同一份图纸问十遍还是同一份），也可以在
// **不该启动任何东西**的地方求：诊断、文档、架构图、以及「这个发行版到底装了
// 什么」这类问题。
//
// # 为什么这个区分值得一个类型
//
// 2026-09-20 加架构图工具时撞上的：想要那张「谁依赖谁」的图，唯一的入口是
// New()，而 New() 会**启动**整张图。于是画第二份发行版的图时炸了——
//
//	quirk: signature 该模型始终思考 already registered
//
// 因为第一个规格书已经把模块起过一遍，全局注册表里已经有它了。这不是工具的
// 毛病，是**框架少了一层**：解析与启动被焊在一起，谁想要前一半就得付后一半。
// 焊在一起的代价平时看不见（组合根确实两件事都要做），只在「只想问一句」的
// 时候露出来——而那时候人往往已经写了第二份实现（把 resolve 抄一遍），
// 从此框架的图与工具的图各长各的。
type Plan struct {
	components []Component
	values     map[string][]any
}

// Resolve 收集组件、校验端口、拓扑排序，**不启动任何东西**。
//
// New() 就是「Resolve 之后逐个 Start」，两张脸对着同一份实现（见 NewContext）：
// 校验与排序只有一份，不会漂移。
func Resolve(loaders ...Loader) (*Plan, error) {
	var components []Component
	for i, loader := range loaders {
		loaded, err := loader.Load()
		if err != nil {
			tracef("%s", i18n.T("assembly: loader {index} failed: {err}",
				i18n.A{"index": i + 1, "err": err}))
			return nil, err
		}
		tracef("%s", i18n.T("assembly: loader {index} handed over {count} components",
			i18n.A{"index": i + 1, "count": len(loaded)}))
		components = append(components, loaded...)
	}
	ordered, values, err := resolve(components)
	if err != nil {
		tracef("%s", i18n.T("assembly: graph build failed: {err}", i18n.A{"err": err}))
		return nil, err
	}
	return &Plan{components: ordered, values: values}, nil
}

// MustResolve 是 Resolve 的 fail-fast 版本（组合根/测试里图必须是好的）。
func MustResolve(loaders ...Loader) *Plan {
	plan, err := Resolve(loaders...)
	if err != nil {
		panic("component: resolve failed: " + err.Error())
	}
	return plan
}

// Components 按**启动顺序**返回组件：被依赖的在前。
func (p *Plan) Components() []Component {
	out := make([]Component, len(p.components))
	copy(out, p.components)
	return out
}

// Names 是上面的名字版（诊断输出用）。
func (p *Plan) Names() []string {
	out := make([]string, 0, len(p.components))
	for _, c := range p.components {
		out = append(out, c.Name)
	}
	return out
}

// Providers 是每个端口由谁提供：capability → 组件名。
//
// 一个端口一个提供者（resolve 校验过）。做成 map 而不是「问某个组件提供了
// 什么」，是因为拿到图的人问的通常是反过来的那句话——「这个能力谁给的」。
func (p *Plan) Providers() map[string]string {
	out := map[string]string{}
	for _, c := range p.components {
		for _, prov := range c.Provides {
			out[prov.Name()] = c.Name
		}
	}
	return out
}

// Dependencies 是这张图的边：组件名 → 它需要的端口（按名字排序，输出稳定）。
//
// 每个需求带上**它是不是 Optional**——这两者的区别正是 review 时最该看见的
// 那一条：Need 断了装配就起不来（硬边），Optional 断了只是「这个功能没有入口」
// （弱边，见 component.Optional）。
type Dependency struct {
	Capability string
	ProvidedBy string // 空 = 这次装配里没人提供（Optional 的常见下场）
	Optional   bool
}

func (p *Plan) Dependencies() map[string][]Dependency {
	providers := p.Providers()
	out := map[string][]Dependency{}
	for _, c := range p.components {
		for _, req := range c.Requires {
			out[c.Name] = append(out[c.Name], Dependency{
				Capability: req.Name(),
				ProvidedBy: providers[req.Name()],
				Optional:   req.Optional(),
			})
		}
		sort.Slice(out[c.Name], func(i, j int) bool {
			return out[c.Name][i].Capability < out[c.Name][j].Capability
		})
	}
	return out
}
