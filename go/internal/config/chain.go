package config

import (
	"fmt"
	"sort"
)

// DefaultPriority profile 没写 priority 时的默认值。
// 取中间值，这样用户既能往前插（更小）也能往后放（更大）。
const DefaultPriority = 50

// Step 链上的一步：用哪个 profile 的哪个绑定。
type Step struct {
	Profile  string
	Binding  Binding
	Provider Provider
}

func (s Step) String() string { return s.Profile + ":" + s.Binding.String() }

// Skip 某个候选为什么没进链。给 explain 视图和 X-Newgate-Chain 用。
type Skip struct {
	Profile string
	Target  string // provider/model，或空表示整个 profile 被跳过
	Reason  string
}

// ChainOpts 构建链需要的外部输入。
type ChainOpts struct {
	Active string // 链头（--set-profile 选中的）
	// Available 提供可用性判断（熔断器）。nil 表示都可用。
	Available func(provider string) bool
	// Disabled 提供禁用判断。nil 表示都没禁。
	// target 形如 "provider/model"，role 用于 role 级禁用。
	Disabled func(role, target string) (bool, string)
	MaxSteps int // 0 表示不限
}

// BuildChain 为某个档位构造 fallback 链。
//
// 顺序：**profile 为主序（链头优先，其余按 priority），list 为次序**。
// 语义是「先在当前任务档位内部找替代，找不到才降到下一个档位」。
//
// 规则：
//   - 链头 = Active。其余按 (priority, name) 排序
//   - excluded 的 profile 不进链（除非它就是 Active——显式选中优先）
//   - Active 是 pinned 时，链只有它自己（不往下掉）
//   - profile 可以稀疏：没定义这个档位就整层跳过
//   - (provider, model) 去重，只试一次
func BuildChain(role string, profiles []*Profile, provs *Providers, o ChainOpts) ([]Step, []Skip) {
	ordered, skips := orderProfiles(profiles, o.Active)

	var steps []Step
	seen := map[string]bool{}

	for _, p := range ordered {
		cands := p.CandidatesFor(role)
		if len(cands) == 0 {
			skips = append(skips, Skip{p.Name, "", "未定义 " + role + " 档位"})
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
				if yes, why := o.Disabled(role, key); yes {
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
func orderProfiles(profiles []*Profile, active string) ([]*Profile, []Skip) {
	var head *Profile
	var rest []*Profile
	var skips []Skip

	for _, p := range profiles {
		if p.Name == active {
			head = p
			continue
		}
		rest = append(rest, p)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		pi, pj := rest[i].Prio(), rest[j].Prio()
		if pi != pj {
			return pi < pj
		}
		return rest[i].Name < rest[j].Name // tiebreak 保证可复现
	})

	var out []*Profile
	if head != nil {
		out = append(out, head) // 显式选中优先，excluded 不挡链头
		if head.Pinned {
			// 不往下掉：链到此为止
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

// LoadChainInputs 一次性把构链需要的东西读齐。
func LoadChainInputs() ([]*Profile, *Providers, *State, error) {
	names, err := ListProfiles()
	if err != nil {
		return nil, nil, nil, err
	}
	var ps []*Profile
	for _, n := range names {
		p, err := LoadProfile(n)
		if err != nil {
			continue // 坏文件跳过，不让整条链死；doctor 会报
		}
		ps = append(ps, p)
	}
	provs, err := LoadProviders()
	if err != nil {
		return nil, nil, nil, err
	}
	return ps, provs, LoadState(), nil
}
