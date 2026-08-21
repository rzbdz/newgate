// Package domain 是实体与值类型。**不含任何 IO**。
//
// 谁都可以依赖它，它不依赖任何人（docs/02-architecture.md §2）。
package domain

import "time"

// Roles 语义档位。工具侧只写这些名字，永不写真实模型名。
//
// TODO(M1+): 现在是「能力档」一个维度。docs/18 与后续讨论要引入
// 「功能」维度（planner / executor / thinker / reviewer …），
// 两者是正交的：profile 只声明 2-3 个能力档，另有一张功能→档位映射表，
// 这样只有两个模型的 provider 家族也只需写两行。
var Roles = []string{"heavy", "mid", "light", "vision"}

const (
	// DefaultPriority profile 没写 priority 时的默认值。取中间值，
	// 用户既能往前插（更小）也能往后放（更大）。
	DefaultPriority = 50
	ProxyPort       = 8899
)

// Provider 一个上游账号。
type Provider struct {
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // 优先于 APIKey
	Protocol  string `json:"protocol,omitempty"`    // openai | anthropic，默认 openai
	// TODO(M2): Models 声明这个 provider 提供哪些模型。
	// 有了它才能解析「客户端直接请求具体模型名」的情况——那时链只在
	// 提供该确切模型的 provider 之间流转，绝不换成别的模型（docs/18 §5）。
	Models []string `json:"models,omitempty"`
}

type Providers struct {
	Providers map[string]Provider `json:"providers"`
}

// Binding 一个具体的 (provider, model) 对。
type Binding struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (b Binding) String() string { return b.Provider + "/" + b.Model }

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
}

func (p *Profile) Prio() int {
	if p.Priority == nil {
		return DefaultPriority
	}
	return *p.Priority
}

// CandidatesFor 返回这个 profile 为某档位提供的候选列表（空 = 稀疏）。
func (p *Profile) CandidatesFor(role string) Candidates {
	if c, ok := p.Roles[role]; ok && len(c) > 0 {
		return c
	}
	if c, ok := p.Roles["*"]; ok && len(c) > 0 {
		return c
	}
	if p.Fallback != nil {
		return Candidates{*p.Fallback}
	}
	return nil
}

// Resolve 取第一个候选。给只需要单个绑定的调用点（status / tui）。
func (p *Profile) Resolve(role string) (Binding, bool) {
	c := p.CandidatesFor(role)
	if len(c) == 0 {
		return Binding{}, false
	}
	return c[0], true
}

// ChainLimits 防止一个请求把整条链串一遍（6 家 × 40s = 4 分钟才失败）。
type ChainLimits struct {
	MaxAttempts   int  `json:"max_attempts"` // 实际发出的请求数上限，跨两级累计
	TotalBudgetMs int  `json:"total_budget_ms"`
	FallbackOn400 bool `json:"fallback_on_400"` // 见 docs/18 §6：默认关
}

func (c ChainLimits) Attempts() int {
	if c.MaxAttempts <= 0 {
		return 3
	}
	return c.MaxAttempts
}

func (c ChainLimits) Budget() int {
	if c.TotalBudgetMs <= 0 {
		return 120000
	}
	return c.TotalBudgetMs
}

// TODO(M3): Session 一次运行实例。BE 要列出活跃会话给前端看。
// 另外「会话粘性」需要它：同一 session 一旦解析出结果就钉住，
// 否则模型在会话内来回跳会让 prompt 缓存反复作废（docs/18 §9）。
type Session struct {
	ID        string
	Tool      string
	Profile   string
	StartedAt string
	// TODO(M3): PID / 注入回滚句柄 / 用量累计
}

// TODO(M4): Endpoint —— provider 下的具体协议入口。
// 有了它「一个 provider 服务多种方言的工具」才成立：同一个账号既暴露
// anthropic 端点又暴露 openai 端点，按工具要求的方言选（docs/01 §3）。
type Endpoint struct {
	Protocol string
	BaseURL  string
	Default  bool
}

// Key 返回这个 provider 的密钥。
//
// APIKeyEnv 的查找需要读环境变量——那是 IO，core 不该做。所以由
// store 层在加载时把 env 里的值填进 APIKey，core 只看最终结果。
// 这是「core 不 import 任何 IO」这条规则的一个具体落地。
func (p Provider) Key() string { return p.APIKey }

// TODO(M2): 换成 secretref.Secret，让明文只在内存中存在且不会被误打印
// （docs/09 §2）。现在是裸字符串，日志脱敏靠 gateway 层的正则兜着。

// State 机器本地的运行时状态。经常改，不进 dotfiles。
type State struct {
	// Active per-agent 链头：每个 agent 独立选自己的 profile。
	// claude 用 expensive、opencode 用 cheap，各切各的——这是 M1 的
	// 「per-agent 切换」。
	Active map[string]string `json:"active,omitempty"`
	// DefaultProfile Active 里没列的 agent 用它。
	DefaultProfile string `json:"default_profile"`

	// ActiveProfile / FallbackProfile 是 v0 的旧字段，保留只为迁移。
	// 旧 state.json 用单一 active_profile 表示链头、fallback_profile 表示
	// 备用。新模型里链头是 default_profile，fallback 由 profile 的 priority
	// 链表达。Normalize() 会把旧字段迁过来。
	ActiveProfile   string `json:"active_profile,omitempty"`
	FallbackProfile string `json:"fallback_profile,omitempty"`

	Port      int  `json:"port"`
	TakenOver bool `json:"taken_over"`

	// Chain 链的成本上界。
	Chain ChainLimits `json:"chain"`

	// Debug 打印每个请求的完整头/体（密钥脱敏）。出错时无论如何都会记全。
	Debug bool `json:"debug"`
	// DebugUntil 自动过期时刻（RFC3339）。debug 单条能记 8KB+，
	// 忘了关会把磁盘写满，所以默认只开一段时间。
	DebugUntil string `json:"debug_until,omitempty"`

	// SchemaRepair 给缺 required 的 tool schema 补 "required": []。
	// 按 JSON Schema 规范这是语义无操作，所以默认开。
	// 用指针以区分「没配」和「显式关闭」。
	SchemaRepair *bool `json:"schema_repair,omitempty"`

	// TODO(M2): Mood / RoleBudgets / Disabled —— 见 core/policy。
}

// Normalize 补默认值。读盘后调用。
func (s *State) Normalize() {
	if s.Port == 0 {
		s.Port = ProxyPort
	}
	// 迁移：v0 的 active_profile → default_profile。
	if s.DefaultProfile == "" && s.ActiveProfile != "" {
		s.DefaultProfile = s.ActiveProfile
	}
	if s.DefaultProfile == "" {
		s.DefaultProfile = "cheap"
	}
	if s.Active == nil {
		s.Active = map[string]string{}
	}
}

// ActiveFor 某个 agent 的链头。没单独设过就用全局默认。
func (s *State) ActiveFor(agent string) string {
	if p, ok := s.Active[agent]; ok && p != "" {
		return p
	}
	return s.DefaultProfile
}

func (s *State) RepairEnabled() bool {
	return s.SchemaRepair == nil || *s.SchemaRepair
}

// DebugActive debug 是否仍在有效期内。过期即视为关闭。
//
// debug 单条请求能记 8KB+（opencode 的系统提示就有 97KB），忘了关会把
// 磁盘写满，所以默认带过期时间。
func (s *State) DebugActive() bool {
	if !s.Debug {
		return false
	}
	if s.DebugUntil == "" {
		return true // 显式永久开
	}
	t, err := time.Parse(time.RFC3339, s.DebugUntil)
	if err != nil {
		return true
	}
	return time.Now().Before(t)
}
