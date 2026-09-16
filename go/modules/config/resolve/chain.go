// Package resolve 是解析流水线：给定配置快照，算出一个档位的 fallback 链。
//
// 全部是纯函数——外部事实（熔断状态、禁用规则、时钟）都作为入参传进来。
// 这样「给定这些配置，应该解析出什么链」可以完全离线测试。
package resolve

import (
	"fmt"
	"sort"

	"github.com/rzbdz/newgate/go/modules/config/domain"
)

// Step 链上的一步：用哪个 profile 的哪个绑定。
type Step struct {
	Profile  string
	Binding  domain.Binding
	Provider domain.Provider
}

func (s Step) String() string { return s.Profile + ":" + s.Binding.String() }

// Skip 某个候选为什么没进链。给 explain 视图和 X-Newgate-Chain 用。
//
// 记录跳过原因不是锦上添花：用户唯一会问的问题是「为什么不是我想的那个」，
// 没有这个就只能靠猜（docs/04-configuration.md）。
type Skip struct {
	Profile string
	Target  string // provider/model；空表示整个 profile 被跳过
	Reason  string
}

// Opts 构建链需要的外部输入。全部以函数形式注入，保持 BuildChain 纯净。
type Opts struct {
	// Active 链头。per-agent：不同 agent 可以有不同的链头。
	Active string
	// Available 可用性判断（熔断器）。nil = 都可用。
	Available func(provider, model string) bool
	// Rank 最近 probe 的健康档。只重排链头之后的 fallback；值越小越优先，
	// 相同值保持 profile/list 的原始顺序。nil = 不动态重排。
	Rank func(provider, model string) int
	// Disabled 禁用判断。nil = 都没禁。target 形如 "provider/model"。
	Disabled func(tier, target string) (bool, string)
	// MaxSteps 0 = 不限。
	MaxSteps int
}

// BuildChain 为某个键构造 fallback 链。
//
// 「键」可以是档位（heavy/normal/mid/light/vision），也可以是模块贡献的
// 动态角色（omo-sisyphus，见 domain.ExtraRole）；解析路径完全同一条。
//
// 顺序：**profile 为主序（链头优先，其余按 priority），list 为次序**。
// 语义是「先在当前任务键内部找替代，找不到才降到下一个 profile」——
// 「我要便宜的」应该先在便宜的里面挑，而不是一失败就跳到贵的。
//
// 规则：
//   - 链头 = Opts.Active；其余按 (priority, name) 排序，name 做 tiebreak
//     保证可复现
//   - excluded 的 profile 不进链，除非它就是链头（显式选中优先）
//   - 链头是 pinned 时链只有它自己（含它内部的 list），不往下掉
//   - profile 可稀疏：没定义这个键就整层跳过（有别名时先走别名，见
//     domain.Profile.CandidatesFor）
//   - 候选里的**引用**（`@normal`）就地展开成那条链，深度优先，带环检测
//   - (provider, model) 全链去重，只试一次
//
// 展开后的结果就是一条扁平的 fallback 序列：所有 profile、所有候选（含引用
// 展开出来的）按「profile 优先级 → list 次序」排好。引用没有引入第二套
// fallback 语义，只是让键之间能互相指。见 docs/04-configuration.md。
func BuildChain(key string, profiles []*domain.Profile, provs *domain.Providers, o Opts) ([]Step, []Skip) {
	ordered, skips := orderProfiles(profiles, o.Active)
	b := &chainBuilder{profiles: ordered, provs: provs, o: o,
		seen: map[string]bool{}, visiting: map[string]bool{key: true}, skips: skips}
	b.expand(key)
	if o.Rank != nil && len(b.steps) > 2 {
		sort.SliceStable(b.steps[1:], func(i, j int) bool {
			a, z := b.steps[i+1], b.steps[j+1]
			return o.Rank(a.Binding.Provider, a.Binding.Model) <
				o.Rank(z.Binding.Provider, z.Binding.Model)
		})
	}
	if o.MaxSteps > 0 && len(b.steps) > o.MaxSteps {
		for _, s := range b.steps[o.MaxSteps:] {
			b.skips = append(b.skips, Skip{s.Profile, s.Binding.String(),
				fmt.Sprintf("超出 maxAttempts=%d", o.MaxSteps)})
		}
		b.steps = b.steps[:o.MaxSteps]
	}
	return b.steps, b.skips
}

type chainBuilder struct {
	profiles []*domain.Profile
	provs    *domain.Providers
	o        Opts
	seen     map[string]bool
	visiting map[string]bool // 正在展开的键（防引用成环）
	steps    []Step
	skips    []Skip
}

// expand 把 key 的候选按 profile 优先、list 次序展开进 steps。
func (b *chainBuilder) expand(key string) {
	for _, p := range b.profiles {
		cands := p.CandidatesFor(key)
		if len(cands) == 0 {
			b.skips = append(b.skips, Skip{p.Name, "", "未定义 " + key})
			continue
		}
		for _, bd := range cands {
			if bd.IsRef() {
				if b.visiting[bd.Ref] {
					b.skips = append(b.skips, Skip{p.Name, bd.String(),
						"引用成环（" + key + " → " + bd.Ref + "），已跳过"})
					continue
				}
				b.visiting[bd.Ref] = true
				b.expand(bd.Ref)
				delete(b.visiting, bd.Ref)
				continue
			}
			b.add(p, key, bd)
		}
	}
}