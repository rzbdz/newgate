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

// BuildChain 为某个档位构造 fallback 链。
//
// 顺序：**profile 为主序（链头优先，其余按 priority），list 为次序**。
// 语义是「先在当前任务档位内部找替代，找不到才降到下一个 profile」——
// 「我要便宜的」应该先在便宜的里面挑，而不是一失败就跳到贵的。
//
// 规则：
//   - 链头 = Opts.Active；其余按 (priority, name) 排序，name 做 tiebreak
//     保证可复现
//   - excluded 的 profile 不进链，除非它就是链头（显式选中优先）
//   - 链头是 pinned 时链只有它自己（含它内部的 list），不往下掉
//   - profile 可稀疏：没定义这个档位就整层跳过
//   - (provider, model) 全链去重，只试一次
func BuildChain(tier string, profiles []*domain.Profile, provs *domain.Providers, o Opts) ([]Step, []Skip) {
	ordered, skips := orderProfiles(profiles, o.Active)

	var steps []Step
	seen := map[string]bool{}

	for _, p := range ordered {
		cands := p.CandidatesFor(tier)
		if len(cands) == 0 {
			skips = append(skips, Skip{p.Name, "", "未定义 " + tier + " 档位"})
			continue
		}
		for _, b := range cands {
			key := b.String()
			if seen[key] {
				skips = append(skips, Skip{p.Name, key, "与链上更前面的重复，已去重"})
				continue
			}
			seen[key] = true

			prov, ok := provs.Providers[b.Provider]
			if !ok {
				skips = append(skips, Skip{p.Name, key, "provider 未定义"})
				continue
			}
			if prov.Key() == "" {
				skips = append(skips, Skip{p.Name, key, "provider 没有 api_key"})
				continue
			}
			if o.Disabled != nil {
				if yes, why := o.Disabled(tier, key); yes {
					skips = append(skips, Skip{p.Name, key, "已禁用：" + why})
					continue
				}
			}
			if o.Available != nil && !o.Available(b.Provider) {
				skips = append(skips, Skip{p.Name, key, "熔断中"})
				continue
			}
			steps = append(steps, Step{Profile: p.Name, Binding: b, Provider: prov})
		}
	}

	if o.MaxSteps > 0 && len(steps) > o.MaxSteps {
		for _, s := range steps[o.MaxSteps:] {
			skips = append(skips, Skip{s.Profile, s.Binding.String(),
				fmt.Sprintf("超出 maxAttempts=%d", o.MaxSteps)})
		}
		steps = steps[:o.MaxSteps]
	}
	return steps, skips
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
