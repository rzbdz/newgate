package domain

import (
	"sort"
	"strings"
)

// ExtraRole 框架之外的**动态角色键**——由模块贡献，core 不认识具体是谁。
//
// 为什么需要这一层：档位（heavy/normal/mid/light/vision）是**能力阶梯**，
// 所有客户端共享；但有些客户端自己有更细的槽位体系——opencode + omo 的
// intra-agent（sisyphus / librarian / …）、将来别的工具的自定义 agent。
// 这些键的数量、名字、默认归属都是**那个模块**的知识，不该 hard-code 进
// core。
//
// 所以分工是：
//
//	模块（如 omo 接管）实现 roleprov.Provider，说自己有哪些键、各自缺省
//	  ref 到哪个键；
//	core 只维护一张「键 → 缺省 ref」的表，解析时与内置别名一视同仁。
//
// 键名进 profile 的写法与档位完全一样（`omo-sisyphus = @normal`），
// 因为解析路径是同一条——见 CandidatesFor / resolve.BuildChain。
type ExtraRole struct {
	// Key 键名。命名冲突由贡献方负责（写进 profile 的也是这个名字）。
	Key string `json:"key"`
	// Source 谁贡献的（模块短名，如 "omo" / "builtin"）。只给人看。
	Source string `json:"source"`
	// Default 这个键在 profile 里**没写**时，等价于哪个键（通常是某个档位）。
	// 空 = 没有缺省，没写就真的没有候选（稀疏）。
	Default string `json:"default,omitempty"`
	// Meta 贡献方自己的说明，core 不解释（如 was / variant / suggested）。
	Meta map[string]string `json:"meta,omitempty"`
}

// builtinAliases 内置的角色等价关系。机制与模块贡献的 ExtraRole 完全一样，
// 只是来源写在代码里——这样「四档化的向下兼容」不需要第二条代码路径。
var builtinAliases = []ExtraRole{{
	Key: "normal", Source: "builtin", Default: "mid",
	Meta: map[string]string{"why": "四档化（2026-09-16）的向下兼容：没写 normal 的老配置等价于 mid"},
}}

var extraRoles []ExtraRole

// SetExtraRoles 灌入模块贡献的动态角色键。启动时调一次（见
// runtime/roleprov.Refresh）；传 nil 回到「只有内置别名」。
func SetExtraRoles(rs []ExtraRole) {
	extraRoles = rs
}

// ExtraRoles 当前全部动态角色键（内置别名在前，模块的按 key 排序在后）。
func ExtraRoles() []ExtraRole {
	out := make([]ExtraRole, 0, len(builtinAliases)+len(extraRoles))
	out = append(out, builtinAliases...)
	mods := make([]ExtraRole, len(extraRoles))
	copy(mods, extraRoles)
	sort.Slice(mods, func(i, j int) bool { return mods[i].Key < mods[j].Key })
	return append(out, mods...)
}

// ExtraRoleOf 这个键是不是动态角色键。
func ExtraRoleOf(key string) (ExtraRole, bool) {
	for _, r := range builtinAliases {
		if r.Key == key {
			return r, true
		}
	}
	for _, r := range extraRoles {
		if r.Key == key {
			return r, true
		}
	}
	return ExtraRole{}, false
}

// AliasFor 这个键在 profile 里没写时等价于哪个**键**（"" = 不是键到键的等价）。
// 只是给展示/校验用；解析走 DefaultBindingFor。
func AliasFor(key string) string {
	r, ok := ExtraRoleOf(key)
	if !ok {
		return ""
	}
	d := strings.TrimSpace(r.Default)
	d = strings.TrimPrefix(d, "@")
	if d == "" || strings.Contains(d, "/") {
		return "" // 具体绑定（provider/模型）不是键的等价
	}
	return d
}

// DefaultBindingFor 这个键在 profile 里没写时，等价于哪条绑定。
//
// Default 支持三种写法，空 = 没有缺省（稀疏，整层跳过）：
//
//	"@别的键"        引用：就地展开那条链。最常用，也是模块贡献键的默认形态
//	                 （omo-sisyphus = @normal：跟进主力档，档位一动它跟着动）
//	"provider/模型"  具体绑定：钉到某条真实模型（不会随档位漂移）
//	"别的键"         等价于那个键——内置别名 normal→mid 就是这么写的一条记录
//
// 三种都归一成 Binding 再返回，于是**解析路径只有一条**：引用本来就是候选
// 的第一等公民（chainBuilder.expand 展开它、去重、环检测），别名不必再养
// 一套「去别的键里翻候选人」的第二路径。
func DefaultBindingFor(key string) (Binding, bool) {
	r, ok := ExtraRoleOf(key)
	if !ok {
		return Binding{}, false
	}
	d := strings.TrimSpace(r.Default)
	if strings.HasPrefix(d, "@") {
		d = strings.TrimSpace(d[1:])
		if d == "" {
			return Binding{}, false
		}
		return Binding{Ref: d}, true
	}
	if d == "" {
		return Binding{}, false
	}
	if strings.Contains(d, "/") {
		if bd, err := ParseBindingString(d); err == nil {
			return bd, true
		}
		return Binding{}, false
	}
	return Binding{Ref: d}, true
}

// IsKnownRole 客户端发来的 model 字段是不是一个**角色键**（档位或动态角色）。
// 不是的话就按「具体模型名」反查（docs/04-configuration.md）。
func IsKnownRole(s string) bool {
	if IsRole(s) {
		return true
	}
	_, ok := ExtraRoleOf(s)
	return ok
}

// TierLadder 阶梯档由弱到强。vision 是**正交**档（看得见图，不代表更聪明），
// 不在阶梯上，也就不能靠「升一级」从别的档走过来。
var TierLadder = []string{"light", "mid", "normal", "heavy"}

// ShiftTier 在阶梯上挪 n 级（越界夹住）。不是阶梯档（vision / 未知）原样返回。
// 用途：omo 槽位的 `variant`（max/high/low）表达的是「这条槽位调多猛」，
// 算建议档位时把它折进体格（docs/04-configuration.md）。
func ShiftTier(tier string, n int) string {
	if n == 0 {
		return tier
	}
	i := -1
	for j, t := range TierLadder {
		if t == tier {
			i = j
			break
		}
	}
	if i < 0 {
		return tier
	}
	i += n
	if i < 0 {
		i = 0
	}
	if i >= len(TierLadder) {
		i = len(TierLadder) - 1
	}
	return TierLadder[i]
}
