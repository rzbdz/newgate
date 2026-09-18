package gateway

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/gateway/policy"
	"github.com/rzbdz/newgate/go/modules/gateway/quirk"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// Request 是 special treatment 判断请求上下文所需的稳定事实集合。
// 它不暴露转发器内部对象，插件因而不能控制重试、连接或响应生命周期。
// 契约本体在 special（插件层）定义，这里只做名字转发。
type Request = special.Request

// Plugin 是网关请求改写扩展点。Match 负责缩小适用范围，
// Apply 只做字节级手术并返回可记录的 notes；错误由调用方按 fail-open 处理。
type Plugin = special.Plugin

// 数据面策略扩展点的类型转发（本体在 policy，理由写在那个包的注释里：
// 数据面从自己的状态机里读出四个决策点，gateway 因此不认识任何一家策略）。
type (
	Filter           = policy.Filter
	Admitter         = policy.Admitter
	Adjudicator      = policy.Adjudicator
	Controller       = policy.Controller
	Flusher          = policy.Flusher
	Env              = policy.Env
	EnvBinder        = policy.EnvBinder
	MetricNamer      = policy.MetricNamer
	Outcome          = policy.Outcome
	OutcomeKind      = policy.OutcomeKind
	Verdict          = policy.Verdict
	Evidence         = policy.Evidence
	ProbeObservation = policy.ProbeObservation
	ProbeAck         = policy.ProbeAck
)

// 结局的种类。数据面说的是事实，不是判断。
const (
	Succeeded        = policy.Succeeded
	ConnectionFailed = policy.ConnectionFailed
	RejectedStatus   = policy.RejectedStatus
	StreamCut        = policy.StreamCut
)

// Gateway 是网关模块公开的控制面：**别人往数据面里插东西的口**。
//
// 它不暴露 handler、不暴露内部 registry，也不暴露任何具体策略的名字——这正是
// 这一层的意义。四条插入口各对应数据面自己的一个决策点（建链准入、结局裁决、
// 控制面自报、停机落盘），见 modules/gateway/policy 的包注释。
type Gateway interface {
	// RegisterRequestHook 插一个请求改写插件（special treatment）。
	RegisterRequestHook(Plugin) (modules.Release, error)

	// RegisterFilter 插一个数据面策略贡献者：准入（Admitter，只允许一个）、
	// 结局裁决（Adjudicator）、控制面自报（Controller）、停机落盘（Flusher），
	// 按需实现其中几个。一个都不实现 = 只想要一个名字。
	//
	// 没有贡献者时数据面照常工作——那是「最小系统」，四个决策点都走内核默认
	// （全部候选可用、只按机制换站、不记账、不落盘）。
	RegisterFilter(Filter) (modules.Release, error)

	// Quirks 是那张「上游毛病」表（见 modules/gateway/quirk）。
	//
	// 它暴露出来是因为**判据得由拥有补丁的模块注册**：`该模型始终思考` 这条
	// 签名的原文是 GLM / DeepSeek / Kimi 各自的方言，补丁（disabled → enabled +
	// reasoning_effort）住在 modules/thinking，所以签名也该由它注册进来——
	// 与 `special` 的 st-<上游>.go 是同一条规矩：**core 里不出现上游专有字符串**。
	//
	// 数据面在每次请求上把同一张表交给插件（special.Request.Quirks），所以注册
	// 进来的判据对热路径立刻生效。
	Quirks() *quirk.Table
}

// Capability 标识进程中唯一的网关控制面。
var Capability = modules.NewCapability[Gateway]("gateway")
