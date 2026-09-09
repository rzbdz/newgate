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

// IsRole 判断一个模型名是不是语义档位名（而不是具体模型名）。
// 代理收到客户端发来的 model 字段时用它区分「档位路由」和「具体模型路由」
// （docs/18 §5）。
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
	Excluded bool `json:"excluded,omitempty"`
	Roles    map[string]Candidates `json:"roles"`
	// Fallback 本 profile 内所有未定义档位的兜底，等价于 roles["*"]。
	Fallback *Binding `json:"fallback,omitempty"`

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

// Timeouts 网关等上游的时间参数（毫秒）。为什么是配置不是常量：这些值
// 要按上游表现调（比如非流式首字节 12s 是不是太紧），改一次编一次是
// 反模式——写进 state.json，watcher 热加载，改完即生效（docs/06）。
// 全部可省略，缺省用内置默认（见各 accessor）。
type Timeouts struct {
	// FirstByteMs 流式请求等响应头的上限。默认 150s：实测某些通道
	// （anthropic-relay）有固定 ~43s 开销，设太短会把本来能成功的请求
	// 误杀。
	FirstByteMs int `json:"first_byte_ms,omitempty"`
	// FirstByteNonStreamMs 非流式（后台小调用）等响应头的上限。默认
	// 12s（2026-09-09 用户定的起点）：卡住后台调用 = 冻住整个会话——
	// Claude Code 的权限分类器在等，用户终端陪绑。误杀率看
	// `newgate metrics` 的 timeout.first_byte.non_stream 和
	// chain.failover，拿数据再调。
	FirstByteNonStreamMs int `json:"first_byte_non_stream_ms,omitempty"`
	// TotalMs 非流式请求的总超时（流式不设总超时——长响应会被砍断）。
	TotalMs int `json:"total_ms,omitempty"`
}

func (t Timeouts) FirstByte() time.Duration {
	if t.FirstByteMs <= 0 {
		return 150 * time.Second
	}
	return time.Duration(t.FirstByteMs) * time.Millisecond
}

func (t Timeouts) FirstByteNonStream() time.Duration {
	if t.FirstByteNonStreamMs <= 0 {
		return 12 * time.Second
	}
	return time.Duration(t.FirstByteNonStreamMs) * time.Millisecond
}

func (t Timeouts) Total() time.Duration {
	if t.TotalMs <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(t.TotalMs) * time.Millisecond
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

	// ControlToken 控制端点（/__newgate/stop）的 Bearer 令牌。
	// 为什么需要它：共享部署里同组用户读得到这份 state（0660），却对
	// 别人起的 daemon 没有 kill() 权限——停机只能靠代理自己的 HTTP 端点，
	// 而端点必须验明来意。令牌和 providers 的 key 同级保密：能读到它的
	// 组员本来就被信任到了「能拿走上游 key」的程度，停机权限不构成新暴露。
	ControlToken string `json:"control_token,omitempty"`

	// Takeover per-agent 接管意愿（期望态）。没有条目 = 想接管，所以
	// `newgate start` 默认全面接管；显式 false = 用户 `newgate off <agent>`
	// 过它，start 也不该再碰它。
	//
	// 为什么必须持久化：接管 claude 用的是 PATH shim，而 stop 必须把它摘掉
	// ——不然 `claude` 还是命中 shim，wrapper 又把代理懒启动回来，等于没停。
	// 但摘掉之后得记得「用户本来是要接管 claude 的」，否则下次 start 起来了
	// 却不接管，claude 静默直连——同一个不对称，只是反了个方向。
	// 期望态让 start = 插上、stop = 拔掉，两边都不丢用户的意图。
	Takeover map[string]bool `json:"takeover,omitempty"`

	// Chain 链的成本上界。
	Chain ChainLimits `json:"chain"`

	// Timeouts 网关等上游的时间参数（热加载，见 Timeouts 的注释）。
	Timeouts Timeouts `json:"timeouts,omitempty"`

	// Debug 打印每个请求的完整头/体（密钥脱敏）。出错时无论如何都会记全。
	Debug bool `json:"debug"`
	// DebugUntil 自动过期时刻（RFC3339）。debug 单条能记 8KB+，
	// 忘了关会把磁盘写满，所以默认只开一段时间。
	DebugUntil string `json:"debug_until,omitempty"`

	// SchemaRepair 给缺 required 的 tool schema 补 "required": []。
	// 按 JSON Schema 规范这是语义无操作，所以默认开。
	// 用指针以区分「没配」和「显式关闭」。
	SchemaRepair *bool `json:"schema_repair,omitempty"`

	// SpecialTreatment special_treatment 插件层总开关（默认开）。
	// 插件只对认领的上游生效（gateway/special），所以开着不影响别人。
	SpecialTreatment *bool `json:"special_treatment,omitempty"`
	// SpecialOff 单独关掉的插件名。排查「是不是 newgate 改坏了请求」时
	// 关掉某一个比关掉整层更精确。名字见 `newgate st`。
	SpecialOff []string `json:"special_treatment_off,omitempty"`

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

// TakeoverWanted 用户是否希望接管这个 agent。没表态过就算想要——
// `newgate start` 的语义是「全面接管」，不该要求用户先逐个登记。
func (s *State) TakeoverWanted(agent string) bool {
	if v, ok := s.Takeover[agent]; ok {
		return v
	}
	return true
}

func (s *State) RepairEnabled() bool {
	return s.SchemaRepair == nil || *s.SchemaRepair
}

// SpecialEnabled special_treatment 层是否启用。默认开。
func (s *State) SpecialEnabled() bool {
	return s.SpecialTreatment == nil || *s.SpecialTreatment
}

// SpecialPluginOff 某个插件是否被单独关掉。
func (s *State) SpecialPluginOff(name string) bool {
	for _, n := range s.SpecialOff {
		if n == name {
			return true
		}
	}
	return false
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
