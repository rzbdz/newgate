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

	modules "github.com/rzbdz/newgate/go/component"
)

// ProbeGrade 把探活结果归一为少量稳定档位，供路由排序和 UI 共同解释。
type ProbeGrade string

const (
	ProbeFluent      ProbeGrade = "fluent"
	ProbeUsable      ProbeGrade = "usable"
	ProbeLaggy       ProbeGrade = "laggy"
	ProbeUnavailable ProbeGrade = "unavailable"
)

// Status 是 binding 健康表的一行，也是 daemon→CLI 的 wire 契约
// （`/__newgate/status` 的 `breakers` 字段）。
//
// **只能加字段，不能改字段。** 优雅交接期间 CLI 与 daemon 可以来自不同版本：
// 新 CLI 读旧 daemon 的快照、旧 CLI 读新 daemon 的快照都必须能活。同理
// `health.json` 的文件名与这些 JSON 键也不能改——降级回滚用的 known-good
// 二进制读的就是它们。
type Status struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Fails    int           `json:"fails"`
	Open     bool          `json:"open"`
	OpenFor  time.Duration `json:"open_for_ns"`
	Reason   string        `json:"reason,omitempty"`
	Grade    ProbeGrade    `json:"grade,omitempty"`
	Latency  int64         `json:"latency_ms,omitempty"`
	Checked  time.Time     `json:"checked_at,omitempty"`
	ScoreMs  int           `json:"score_ms"`
	Samples  int           `json:"samples"`
	Scores   [4]int        `json:"scores_ms,omitempty"`
	Buckets  [4]int        `json:"samples_by_bucket,omitempty"`
	OpenedAt time.Time     `json:"opened_at,omitempty"`
}

// Breaker 是健康表端口。数据面只读前两项、只写后四项；渲染层只读 Snapshot。
type Breaker interface {
	// Available 回答这个 binding 现在能不能进候选链。建链期调用，只读。
	Available(provider, model string) bool
	// Rank 是建链期的排序键：预测首字节延迟，越小越靠前。
	Rank(provider, model string, contextBytes int) int

	// RecordSuccess 记一次真实流量成功。
	RecordSuccess(provider, model string)
	// RecordFailure 记一次真实流量失败；返回 true 表示这次把闸打开了。
	RecordFailure(provider, model string) bool
	// ObserveSuccess 只记延迟样本，不动可用性（首字节已经拿到之后调用）。
	ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration)
	// RecordProbe 记录一次主动探活的结论，返回评级与是否仍然熔断。
	RecordProbe(provider, model string, status, contextBytes int,
		latency, slowAfter time.Duration, probeErr string) (ProbeGrade, bool)

	// Snapshot 冻结一份可序列化的现状。
	Snapshot() []Status

	// Flush 把节流窗口内尚未落盘的延迟样本同步写出（优雅退出用）。
	Flush()
	// UseFile 让健康表跨优雅重启存活。
	UseFile(path string) error
	// SetErrorHandler 注入持久化错误出口；健康状态不能因写盘失败而静默丢失。
	SetErrorHandler(func(error))
}

// Capability 是健康表在组件图里的身份。
var Capability = modules.NewCapability[Breaker]("breaker")

// NewTable 造一份纯内存健康表（不落盘）。测试与模块装配都用它。
func NewTable() Breaker { return newTable() }
