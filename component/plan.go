package component

import (
	"fmt"
	"sort"
	"strings"

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

// Precomputed 是**自带图纸**的 Loader：它交出来的组件出自一个**编译期就定死的
// 模块集合**（没有 runtime 插拔），启动顺序在构建期已经算好。
//
// # 它为什么存在（2026-09-21）
//
// 用户的原话：「逻辑上，不应该跑个 cli 就触发装配的啊……这个依赖关系是编译期的
// 包，你 runtime 去 resolve 他没用啊，build time 直接生成依赖关系啊。因为他本身
// 就是静态固定的，他没有 runtime 插拔机制啊。」
//
// 他说得对。装的模块在**构建期**就定了（规格书选好、名字写进生成的清单），
// 顺序只是那个集合的函数——所以它是编译期事实。而在这之前，每敲一条 `newgate
// status`、每次起 web 后端，都要把那件事从头推一遍：建端口表、建依赖边、拓扑
// 排序、查环，再把 24 个组件的声明和 55 条边写进日志。
//
// 装上这个口之后，那条路上只剩「按图纸摆好、从各自的 Provides 收齐端口值」——
// 不建边、不排序、不查环、不写那些日志行。
//
// # 那校验去哪了
//
// 去**构建期**了，而且这才是它该在的地方：tools/distgen 在生成这份顺序时会跑一遍
// 真的 Resolve，装不起来的规格书从此**构建就失败**，而不是等谁敲第一条命令。
// 「重复的组件名」「Need 的端口没人提供」「图里有环」这三类错，构建期报出来比
// 运行期报出来好得多——前者离「谁改坏了哪一行」只隔一次 build。
//
// 顺序与集合对不上时（生成的清单过期了），这里仍然**报错**：那是唯一必须在运行期
// 拦的一条，因为它的症状（某个模块没启动）离原因太远。见 planFromOrder。
type Precomputed interface {
	Loader
	// PrecomputedOrder 返回组件名，按启动顺序（被依赖的在前）。
	// nil = 这份 Loader 没有图纸，运行期照常解析。
	PrecomputedOrder() []string
}

// Resolve 收集组件、校验端口、拓扑排序，**不启动任何东西**。
//
// New() 就是「Resolve 之后逐个 Start」，两张脸对着同一份实现（见 NewContext）：
// 校验与排序只有一份，不会漂移。
func Resolve(loaders ...Loader) (*Plan, error) {
	var components []Component
	var precomputed []string
	for i, loader := range loaders {
		loaded, err := loader.Load()
		if err != nil {
			tracef("%s", i18n.T("assembly: loader {index} failed: {err}",
				i18n.A{"index": i + 1, "err": err}))
			return nil, err
		}
		components = append(components, loaded...)
		if p, ok := loader.(Precomputed); ok && len(precomputed) == 0 {
			precomputed = p.PrecomputedOrder()
		}
	}
	if len(precomputed) > 0 {
		// **构建期算好的图纸**：按它摆好，收齐端口值，完。见 Precomputed 的说明。
		return planFromOrder(components, precomputed)
	}
	tracef("%s", i18n.T("assembly: {count} components, no precomputed plan — resolving at run time",
		i18n.A{"count": len(components)}))
	ordered, values, err := resolve(components)
	if err != nil {
		tracef("%s", i18n.T("assembly: graph build failed: {err}", i18n.A{"err": err}))
		return nil, err
	}
	return &Plan{components: ordered, values: values}, nil
}

// planFromOrder 用**构建期算好的顺序**直接出图纸：不建边、不排序、不查环。
//
// 它只做三件事：按名字摆好、收齐每个端口的提供值、核对图纸与源码对得上。
//
// **核对是这里唯一还留着的校验**，理由：生成的清单会过期（改了规格书、加了模块
// 却没重新生成），而过期的症状是「某个模块没启动」或者「启动顺序莫名其妙」——
// 那离「清单过期」太远，运行期必须当场说清楚。其余三类错（重名、缺端口、环）
// 都在构建期由 distgen 那一趟真的 Resolve 拦住了。
//
// # 为什么要核到**边**，而不只是集合（2026-11-18）
//
// 一条已装配的依赖改成 `Need`（或新增一条 `Need`）而清单没重生成时，**组件集合
// 一个都没变**：旧的校验到「名字集合相等」就放行了，于是按旧顺序启动——消费者
// 可能排在提供者前面，`MustGet` 拿到零值或当场 panic。这条路径是整个进程的
// **默认路径**（每一条 CLI 都走它），所以「顺序只由生成器保证」等于说这条不变式
// 没有人守。代价是一次 O(边) 的扫描，买回「提供者一定先启动」这条唯一的硬保证。
func planFromOrder(components []Component, order []string) (*Plan, error) {
	if len(order) != len(components) {
		return nil, fmt.Errorf("precomputed plan lists %d components but %d were assembled "+
			"— the generated manifest is out of date (rerun the generator)",
			len(order), len(components))
	}
	byName := make(map[string]Component, len(components))
	for _, c := range components {
		byName[c.Name] = c
	}
	out := make([]Component, 0, len(components))
	values := map[string][]any{}
	providedBy := map[string][]string{}
	seen := make(map[string]bool, len(components))
	for _, name := range order {
		c, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("precomputed plan names %q, which was not assembled "+
				"— the generated manifest is out of date (rerun the generator)", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("precomputed plan names %q twice", name)
		}
		seen[name] = true
		for _, prov := range c.Provides {
			values[prov.spec.name] = append(values[prov.spec.name], prov.value)
			providedBy[prov.spec.name] = append(providedBy[prov.spec.name], c.Name)
		}
		out = append(out, c)
	}
	// Replay the graph's edges against the positions we just assigned. A need whose
	// provider sits later (or is missing) is a stale plan — say so here, by name,
	// instead of letting the consumer quietly receive nil.
	pos := make(map[string]int, len(out))
	for i, c := range out {
		pos[c.Name] = i
	}
	for i, c := range out {
		for _, req := range c.Requires {
			providers := providedBy[req.spec.name]
			if len(providers) == 0 {
				// No provider at all: fatal for Need, fine for Optional. A precomputed
				// plan is only ever generated from a successful Resolve, so a missing
				// Need means the set drifted — same message family as below.
				if req.optional {
					continue
				}
				return nil, fmt.Errorf("precomputed plan: %s needs port %q but nothing "+
					"in the assembled graph provides it — the generated manifest is out "+
					"of date (rerun the generator)", c.Name, req.spec.name)
			}
			for _, provider := range providers {
				j, ok := pos[provider]
				if !ok {
					return nil, fmt.Errorf("precomputed plan: %s needs port %q, provided "+
						"by %s, which is not in the plan — the generated manifest is out "+
						"of date (rerun the generator)", c.Name, req.spec.name, provider)
				}
				// Optional is a weak dependency but still an ordering edge when the
				// provider is present (see Optional's comment), so backwards is stale
				// either way — the module would Start before what it waits for.
				if j >= i {
					return nil, fmt.Errorf("precomputed plan: %s (position %d) needs port "+
						"%q, but its provider %s starts at position %d — the generated "+
						"manifest is out of date (rerun the generator)", c.Name, i,
						req.spec.name, provider, j)
				}
			}
		}
	}
	reportMultipleProviders(providedBy)
	tracef("%s", i18n.T("assembly: precomputed plan used — {count} components, no resolution",
		i18n.A{"count": len(out)}))
	return &Plan{components: out, values: values}, nil
}

// reportMultipleProviders names every port with more than one provider.
//
// Multiple providers are legal (a port declares no cardinality), but the consequence
// is not obvious: Get takes the first in start order, so the others are unreachable
// through it and only visible via GetAll. A distribution that accidentally ships a
// second implementation would otherwise change what some commands hit with nobody
// saying so — one trace line is the cheapest place to make that visible.
func reportMultipleProviders(providedBy map[string][]string) {
	for port, providers := range providedBy {
		if len(providers) < 2 {
			continue
		}
		tracef("%s", i18n.T("graph: port {port} has {count} providers in start order: {providers} "+
			"— Get returns the first; use GetAll for the rest",
			i18n.A{"port": port, "count": len(providers), "providers": strings.Join(providers, ", ")}))
	}
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
// 端口**不声明基数**（见 Capability 的注释），所以一个端口可能有多个提供者。
// 这里报的是 **Get 会拿到的那一个**——启动顺序里的第一个，与 agent 实际收到的
// 一致。以前这里是 last-wins（后写的覆盖先写的），而 Get 是 first-wins：装了
// 第二个同端口提供者时，架构图与 `st` 诊断会指着一个**根本没在处理请求**的模块。
// 两个说法说同一件事时不许各说各的。
//
// 需要全部提供者请用 AllProviders（诊断里「这个端口有几个人在抢」）。
func (p *Plan) Providers() map[string]string {
	out := map[string]string{}
	for _, c := range p.components {
		for _, prov := range c.Provides {
			if _, seen := out[prov.Name()]; !seen {
				out[prov.Name()] = c.Name
			}
		}
	}
	return out
}

// AllProviders 是每个端口的**全部**提供者，按启动顺序（Get 取第一个）。
//
// 存在的理由：多个提供者不是错误（端口不声明基数），但「有几个、分别是谁」
// 必须是可观测的——否则一个发行版多装了一个同端口模块，症状是「某些命令忽然
// 走了另一个实现」，而任何地方都不说这件事。
func (p *Plan) AllProviders() map[string][]string {
	out := map[string][]string{}
	for _, c := range p.components {
		for _, prov := range c.Provides {
			out[prov.Name()] = append(out[prov.Name()], c.Name)
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
