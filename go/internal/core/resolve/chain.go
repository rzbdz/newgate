// Package resolve 是解析流水线：给定配置快照，算出一个档位的 fallback 链。
//
// 全部是纯函数——外部事实（熔断状态、禁用规则、时钟）都作为入参传进来。
// 这样「给定这些配置，应该解析出什么链」可以完全离线测试。
package resolve

import (
	"fmt"
	"sort"

	"github.com/rzbdz/newgate/go/internal/core/domain"
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
// 没有这个就只能靠猜（docs/18 §10）。
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
	Available func(provider string) bool
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
// fallback 语义，只是让键之间能互相指。见 docs/18 §1.2。
func BuildChain(key string, profiles []*domain.Profile, provs *domain.Providers, o Opts) ([]Step, []Skip) {
	ordered, skips := orderProfiles(profiles, o.Active)
	b := &chainBuilder{profiles: ordered, provs: provs, o: o,
		seen: map[string]bool{}, visiting: map[string]bool{key: true}, skips: skips}
	b.expand(key)
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

// add 一条具体候选过准入检查后进链。
func (b *chainBuilder) add(p *domain.Profile, key string, bd domain.Binding) {
	k := bd.String()
	if b.seen[k] {
		b.skips = append(b.skips, Skip{p.Name, k, "与链上更前面的重复，已去重"})
		return
	}
	b.seen[k] = true

	prov, ok := b.provs.Providers[bd.Provider]
	if !ok {
		b.skips = append(b.skips, Skip{p.Name, k, "provider 未定义"})
		return
	}
	if prov.Key() == "" {
		b.skips = append(b.skips, Skip{p.Name, k, "provider 没有 api_key"})
		return
	}
	if b.o.Disabled != nil {
		if yes, why := b.o.Disabled(key, k); yes {
			b.skips = append(b.skips, Skip{p.Name, k, "已禁用：" + why})
			return
		}
	}
	if b.o.Available != nil && !b.o.Available(bd.Provider) {
		b.skips = append(b.skips, Skip{p.Name, k, "熔断中"})
		return
	}
	b.steps = append(b.steps, Step{Profile: p.Name, Binding: bd, Provider: prov})
}

// OverrideChain 构造「覆盖绑定为链头、tier 档链为 fallback」的链。
//
// 覆盖绑定的 provider 未定义 / 没 key / 熔断 / 禁用时覆盖不生效：回落纯
// tier 链并返回 applied=false（顺带记一条 Skip 说明为什么没覆盖上）。成功则
// 把覆盖 Step 插到最前，tier 链里与它同 key 的重复步去掉——「全局覆盖第一
// 优先」落在这里，覆盖 model 挂了才轮到 tier 档的候选。
func OverrideChain(override domain.Binding, tier string, profiles []*domain.Profile,
	provs *domain.Providers, o Opts) ([]Step, []Skip, bool) {

	steps, skips := BuildChain(tier, profiles, provs, o)

	if override.Provider == "" || override.Model == "" {
		return steps, skips, false
	}
	prov, ok := provs.Providers[override.Provider]
	switch {
	case !ok:
		skips = append(skips, Skip{"(classifier_override)", override.String(), "provider 未定义，覆盖不生效"})
		return steps, skips, false
	case prov.Key() == "":
		skips = append(skips, Skip{"(classifier_override)", override.String(), "provider 没有 api_key，覆盖不生效"})
		return steps, skips, false
	case o.Available != nil && !o.Available(override.Provider):
		skips = append(skips, Skip{"(classifier_override)", override.String(), "熔断中，覆盖不生效"})
		return steps, skips, false
	}
	if o.Disabled != nil {
		if yes, why := o.Disabled(tier, override.String()); yes {
			skips = append(skips, Skip{"(classifier_override)", override.String(), "已禁用：" + why})
			return steps, skips, false
		}
	}

	ovStep := Step{Profile: "(classifier_override)", Binding: override, Provider: prov}
	out := []Step{ovStep}
	for _, s := range steps {
		if s.Binding.String() == override.String() {
			continue // 已经在链头
		}
		out = append(out, s)
	}
	return out, skips, true
}

// orderProfiles 链头 + 其余按 (priority, name)。
func orderProfiles(profiles []*domain.Profile, active string) ([]*domain.Profile, []Skip) {
	var head *domain.Profile
	var rest []*domain.Profile
	var skips []Skip

	for _, p := range profiles {
		if p.Name == active {
			head = p
			continue
		}
		rest = append(rest, p)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if pi, pj := rest[i].Prio(), rest[j].Prio(); pi != pj {
			return pi < pj
		}
		return rest[i].Name < rest[j].Name
	})

	var out []*domain.Profile
	if head != nil {
		out = append(out, head) // 显式选中优先，excluded 挡不住链头
		if head.Pinned {
			for _, p := range rest {
				skips = append(skips, Skip{p.Name, "",
					fmt.Sprintf("链头 %s 是 pinned，不往下掉", head.Name)})
			}
			return out, skips
		}
	}
	for _, p := range rest {
		if p.Excluded {
			skips = append(skips, Skip{p.Name, "", "excluded：只能显式选中"})
			continue
		}
		out = append(out, p)
	}
	return out, skips
}
