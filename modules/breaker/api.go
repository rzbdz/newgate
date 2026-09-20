// Package breaker 拥有「这条 binding 现在能不能用」这件事的全部判断。
//
// 它从一个 gateway 内部包提升为顶级模块（2026-09-17），因为读它的地方比写它的
// 地方多：数据面（gateway/forward）在建链期问它、控制端点回报探活结论、CLI 把
// 它渲染成 `newgate breaker` / `newgate metrics` / `newgate tier`。放在 gateway
// 里的时候，这些消费者只能通过进程级单例 `health.Default` 摸到它——一个全局
// 可变量，测试之间互相污染，也没法在装配时换掉。现在它经 capability 注入，
// 谁需要谁声明。
//
// 本包**不依赖任何业务模块**：它只知道 binding 键（provider/model）、状态码和
// 延迟。上游专有的知识（什么响应体算「请求形状错误」）由上游模块通过
// RegisterShapeDetector 贡献进来，见 detectors.go。
package breaker

import (
	"time"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/modules/breaker/status"
)

// ProbeGrade / Status 的事实定义在 modules/breaker/status（wire 契约叶子）。
// 这里做别名转发，本包内的代码与老调用方继续用短名字。
type (
	ProbeGrade = status.ProbeGrade
	Status     = status.Status
)

const (
	ProbeFluent      = status.ProbeFluent
	ProbeUsable      = status.ProbeUsable
	ProbeLaggy       = status.ProbeLaggy
	ProbeUnavailable = status.ProbeUnavailable
)

// Breaker 是健康表端口。数据面只读前两项、只写 Report/ObserveSuccess；
// 渲染层只读 Snapshot。
type Breaker interface {
	// Available 回答这个 binding 现在能不能进候选链。建链期调用。
	//
	// 它有副作用：冷却期满时会把 binding 推进半开，并发放这一轮的试探名额。
	// 建链期每个候选只问一次，所以「放行一次真实请求」在这里天然成立。
	Available(provider, model string) bool
	// Rank 是建链期的排序键：预测首字节延迟，越小越靠前。
	Rank(provider, model string, contextBytes int) int

	// Report 回报一次上游交互的结局，返回判决（要不要沿链走）。
	// 分类与状态迁移都在实现里，数据面只如实描述发生了什么。
	Report(provider, model string, in Input) Result
	// ObserveSuccess 只记延迟样本，不动可用性（首字节已经拿到之后调用）。
	ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration)
	// RecordProbe 记录一次主动探活的结论，返回评级与是否仍然熔断。
	RecordProbe(provider, model string, status, contextBytes int,
		latency, slowAfter time.Duration, probeErr string) (ProbeGrade, bool)

	// Snapshot 冻结一份可序列化的现状。
	Snapshot() []Status

	// SetPolicy 整份替换熔断策略（零值项按默认补齐）。
	SetPolicy(Policy)
	// Flush 把节流窗口内尚未落盘的延迟样本同步写出（优雅退出用）。
	Flush()
	// UseFile 让健康表跨优雅重启存活。
	UseFile(path string) error
	// SetErrorHandler 注入持久化错误出口；健康状态不能因写盘失败而静默丢失。
	SetErrorHandler(func(error))

	// SetVerifier 注入「上闸前的最后一次诊断」。
	//
	// 连续失败数到达阈值时先别摘：调这个函数做几次主动探活，探活说它还通就
	// 不摘、计数清零。理由是真实现场（2026-09-17）：smt-deepseek 被摘了
	// 很多次，每次 `newgate probe` 都是 fluent——因为真实流量失败的是
	// **第一字节超时**（分类器那条链把 126KB 的 system 塞进 12s 的紧预算），
	// 而探活发的是最小请求，永远探不到这个边界。两者的结论不一致时，谁的
	// 证据更硬？探活是**主动、可控、可重复**的，被动流量则是单点、受上下文
	// 尺寸和排队影响的。摘牌会让用户被悄悄换给别的模型，代价不对称，所以
	// 上闸前必须再要一次主动证据。
	//
	// 返回 true = 这条 binding 仍然可用（不摘）；false = 确认不可用（照摘）。
	// nil（默认）＝不做这一步，行为与以前完全一致。
	SetVerifier(func(provider, model string) bool)

	// RegisterShapeDetector 贡献一条「这个 4xx 是请求形状问题」的判据。
	//
	// 形状错误的定义**属于上游自己**（见 shape.go 的 ShapeDetector）：core 只
	// 提供端口和「永不摘牌、只计数」的策略，判据由知道那家校验规则的模块注册
	// 进来。没有注册任何检测器时，400 一律不记在任何人头上——少认一次只是少
	// 一个计数，误认一次会让真正的可用性故障被放过。
	//
	// 返回只属于本次注册的撤销句柄；consumer 在 Stop 里逆序释放。
	RegisterShapeDetector(ShapeDetector) (modules.Release, error)
}

// Capability 是健康表在组件图里的身份。
var Capability = modules.NewCapability[Breaker]("breaker")

// NewTable 造一份纯内存健康表（不落盘）。测试与模块装配都用它。
func NewTable() Breaker { return newTable() }
