// Package domain 是实体与值类型。**不含任何 IO**。
//
// 谁都可以依赖它，它不依赖任何人（docs/03-architecture.md）。
package domain

import (
	"strings"
	"time"
)

// Roles 语义档位。工具侧只写这些名字，永不写真实模型名。
//
// 四档能力阶梯 + 一个正交的 vision（2026-09-16 从三档扩到四档）：Claude
// 家族自己就是四档，按它对齐，别的家族（只有大中小三个模型）自然映射得下：
//
//	heavy  ← fable   最贵最强，留给明确点名要它的活
//	normal ← opus    **主力**：claude 的 opus 槽、opencode 的 model
//	mid    ← sonnet  分类器 / compact 总结 / subagent
//	light  ← haiku   起标题这类小活
//	vision           多模态，与上面的阶梯正交
//
// 顺序即能力从高到低，别随手调——`newgate status`、doctor、TUI 都按它排。
//
var Roles = []string{"heavy", "normal", "mid", "light", "vision"}

// IsRole 判断一个模型名是不是语义档位名（而不是具体模型名）。
// 代理收到客户端发来的 model 字段时用它区分「档位路由」和「具体模型路由」
// （docs/04-configuration.md）。
func IsRole(s string) bool {
	for _, r := range Roles {
		if r == s {
			return true
		}
	}
	return false
}

const (
	// DefaultPriority profile 没写 priority 时的默认值。取中间值，
	// 用户既能往前插（更小）也能往后放（更大）。
	DefaultPriority = 50
	// ProxyPort 是未显式配置时的本地网关监听端口。
	ProxyPort = 8899
)

// Provider 一个上游账号。
type Provider struct {
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // 优先于 APIKey
	Protocol  string `json:"protocol,omitempty"`    // openai | anthropic，默认 openai

	// AnthropicURL 这个上游的 **Anthropic 方言** base。不填 = 两种方言同一个
	// base（聚合网关都这样）。
	//
	// 为什么需要它：有些上游把两种方言放在不同的 base 上，而且没有互相转发。
	// 火山方舟实测（2026-09-15）：
	//
	//	OpenAI 方言    https://ark.cn-beijing.volces.com/api/coding/v3  /chat/completions
	//	Anthropic 方言 https://ark.cn-beijing.volces.com/api/coding     /v1/messages
	//
	// 前者打后者 404（istio-envoy 路由级 404，空 body），反过来也一样。转发层
	// 是纯字节直通、不做协议转换（docs/01-product.md），客户端发什么方言就发什么方言，
	// 所以「发给哪个 base」只能按**客户端这次说的方言**选——就是这里的用处。
	//
	// 写法跟 ANTHROPIC_BASE_URL 一致（**不含** /v1）：就是你会贴给任何
	// Anthropic 协议客户端工具的那个值，/v1/messages 由 newgate 自己拼。
	// 已经带 /v1 的写法也认（不重复拼）。
	AnthropicURL string `json:"anthropic_url,omitempty"`

	// Models 声明 provider 可提供的具体模型名，供显式模型请求筛选候选；
	// 这类请求只能在提供同名模型的 provider 之间切换，不能改变模型语义。
	Models []string `json:"models,omitempty"`
}

// IsAnthropicPath 这个转发后缀是不是 Anthropic 方言（/messages、
// /messages/count_tokens）。
//
// 判据是**客户端发来的路径**，不是 provider 的 protocol：protocol 说的是
// 「怎么发到上游」（认证方式、走哪个 base），方言说的是「客户端说的是什么」。
// 聚合网关实测两者可以不一致——provider 标 openai，却照样收 /v1/messages。
func IsAnthropicPath(suffix string) bool {
	return suffix == "/messages" || strings.HasPrefix(suffix, "/messages/")
}

// Base 这条后缀该发给哪个上游 base（不带尾部斜杠），不含路径。
func (p Provider) Base(suffix string) string {
	if p.AnthropicURL != "" && IsAnthropicPath(suffix) {
		return strings.TrimRight(p.AnthropicURL, "/")
	}
	return strings.TrimRight(p.BaseURL, "/")
}

// URL 这条后缀的完整上游地址。
//
// 两种方言同 base 时就是 base_url + suffix（聚合网关的老样子）。分开时走
// anthropic_url，并按 ANTHROPIC_BASE_URL 的惯例补上 /v1——方舟那种
// 「…/api/coding」的写法照抄文档就能用，已经带 /v1 的也不重复补。
func (p Provider) URL(suffix string) string {
	if p.AnthropicURL != "" && IsAnthropicPath(suffix) {
		b := p.Base(suffix)
		if !strings.HasSuffix(b, "/v1") {
			b += "/v1"
		}
		return b + suffix
	}
	return strings.TrimRight(p.BaseURL, "/") + suffix
}

// Providers 是 providers.json 的根对象；显式包一层可为格式演进保留空间。
type Providers struct {
	Providers map[string]Provider `json:"providers"`
}

// Binding 一个具体的 (provider, model) 对，或者一条**引用**（Ref 非空）。
type Binding struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Ref 非空 = 这一条不是具体绑定，而是引用另一个键（写作 `@normal` /
	// `{"ref":"normal"}`）：解析时把那个键的链**就地展开**在这一位。
	// 让「一个槽位绑定到某条档位链」和「槽位自己是一条链」用同一套语法。
	// 展开规则见 docs/04-configuration.md、实现见 resolve.BuildChain。
	Ref string `json:"ref,omitempty"`
}

// String 返回配置和诊断统一使用的可读绑定形式。
func (b Binding) String() string {
	if b.Ref != "" {
		return "@" + b.Ref
	}
	return b.Provider + "/" + b.Model
}

// IsRef 这一条是引用而不是具体绑定。
func (b Binding) IsRef() bool { return b.Ref != "" }

// Profile 一套档位绑定 + 它在 fallback 链里的位置。
// 可以是**稀疏的**——只定义关心的档位，其余跳到链上下一个 profile。
type Profile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Priority 越小越靠前。不写按 DefaultPriority 算。
	Priority *int `json:"priority,omitempty"`
	// Pinned 「我当链头时，链到我为止」——不好用就报错，别偷偷换。
	Pinned bool `json:"pinned,omitempty"`
	// Excluded 「别人别自动掉到我这」——只能被显式选中。
	Excluded bool                  `json:"excluded,omitempty"`
	Roles    map[string]Candidates `json:"roles"`
	// Fallback 本 profile 内所有未定义档位的兜底，等价于 roles["*"]。
	Fallback *Binding `json:"fallback,omitempty"`

	// Extends 让派生 profile 只写差异项，其余从 base profile 继承。
	// 合并规则集中在 MergeFrom，避免 store 与路由各自解释继承。
	Extends string `json:"extends,omitempty"`

	// ContextWindow 主力模型的真实上下文窗口（token 数），>0 才生效。
	// 为什么需要：代理给客户端注入的是真实模型名（glm-5.3），不在
	// Claude Code 的内置模型目录里，客户端按「未知模型」假设 200k 窗口，
	// 动不动提前 compact。设了就在启动时注入 CLAUDE_CODE_MAX_CONTEXT_TOKENS。
	ContextWindow int `json:"context_window,omitempty"`
	// AutoCompactWindow auto-compact 的目标窗口，>0 才生效，应 ≤
	// ContextWindow（客户端取 min）。注入 CLAUDE_CODE_AUTO_COMPACT_WINDOW
	// ——它在客户端解析优先级最高、不依赖账号状态，/context 里会显示
	// "(from CLAUDE_CODE_AUTO_COMPACT_WINDOW)"。
	AutoCompactWindow int `json:"auto_compact_window,omitempty"`
}

// MergeFrom 把 base 的未覆盖项补进来（Extends 的合并规则，store 加载时调用）。
//
// 规则：
//   - 标量（description/priority/fallback/窗口声明）：自己没写（零值）取
//     base 的；
//   - roles：按档位覆盖，base 有、自己没提的档位原样继承；
//   - 裸模型名（Provider 为空的绑定）从 base 同档位**借 provider**——
//     extends=kimi 时写 mid=kimi-k2.7-code-highspeed 就够了；
//   - bool（pinned/excluded）**不继承**：那是这个 profile 自己的态度，
//     不是家族属性。想让变体也被排除，就在变体里再写一遍。
func (p *Profile) MergeFrom(base *Profile) {
	if p == nil || base == nil {
		return
	}
	if p.Description == "" {
		p.Description = base.Description
	}
	if p.Priority == nil {
		p.Priority = base.Priority
	}
	if p.Fallback == nil {
		p.Fallback = base.Fallback
	}
	if p.ContextWindow == 0 {
		p.ContextWindow = base.ContextWindow
	}
	if p.AutoCompactWindow == 0 {
		p.AutoCompactWindow = base.AutoCompactWindow
	}
	if len(base.Roles) > 0 {
		merged := make(map[string]Candidates, len(base.Roles)+len(p.Roles))
		for k, v := range base.Roles {
			merged[k] = v
		}
		for k, v := range p.Roles {
			merged[k] = fillBareProviders(v, merged[k])
		}
		p.Roles = merged
	}
}

// fillBareProviders 给「只有模型名、没写 provider」的候选从同档位的
// base 候选借 provider。base 也没有就保持空（校验层会报出来）。
func fillBareProviders(own, base Candidates) Candidates {
	if len(base) == 0 || base[0].Provider == "" {
		return own
	}
	out := make(Candidates, len(own))
	copy(out, own)
	for i, b := range out {
		if b.Provider == "" && b.Model != "" {
			out[i].Provider = base[0].Provider
		}
	}
	return out
}

// Prio 返回显式优先级或稳定默认值，让排序逻辑无需重复处理 nil。
func (p *Profile) Prio() int {
	if p.Priority == nil {
		return DefaultPriority
	}
	return *p.Priority
}

// CandidatesFor 返回这个 profile 为某个键提供的候选列表（空 = 稀疏）。
//
// 「键」既可以是档位（heavy…），也可以是模块贡献的动态角色键（omo-sisyphus）。
// normal→mid 是 profile 内兼容：老 profile 没写 normal 时，复用自己的 mid。
// 模块贡献的槽位缺省则是跨 profile 的引用，由 BuildChain 展开。
//
// 注意缺省可能是**引用**（omo-sisyphus 缺省 @normal），继续展开是 BuildChain
// 的事（带环检测与去重）——这里只是把「等价于谁」翻译成候选的第一项。
func (p *Profile) CandidatesFor(role string) Candidates {
	if c, ok := p.Roles[role]; ok && len(c) > 0 {
		return c
	}
	// normal 是 2026-09-16 后加的主力档。兼容老配置时必须只借当前
	// profile 的 mid；若返回 @mid，BuildChain 会对每个 profile 反复展开
	// 整条跨-profile mid 链，制造大量假重复，还会破坏稀疏 profile 语义。
	if role == "normal" {
		if c, ok := p.Roles["mid"]; ok && len(c) > 0 {
			return c
		}
	}
	if bd, ok := DefaultBindingFor(role); ok {
		// normal 的内置缺省已在上面按 profile 处理。当前 profile 连 mid
		// 都没有时，应继续走它自己的通配/fallback，而不是展开全局 mid 链。
		if role == "normal" && bd.Ref == "mid" {
			goto profileFallback
		}
		return Candidates{bd}
	}
profileFallback:
	if c, ok := p.Roles["*"]; ok && len(c) > 0 {
		return c
	}
	if p.Fallback != nil {
		return Candidates{*p.Fallback}
	}
	return nil
}