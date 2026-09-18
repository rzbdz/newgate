package metrics

import (
	"strings"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// Group 返回计数器归属的组。分组是给人看的锚点——一眼扫过就知道「有没有在
// 换人」「有没有超时」，不用逐个读计数器名。
//
// **为什么这张表住在这里而不是 CLI**：这些名字是转发热路径产生的（全部由
// gateway/forward 里的 metrics.Default.Inc 写下），含义只有数据面知道。CLI 的
// `newgate metrics` 是那一屏表格的**排版者**，它不该知道 "chain.failover"
// 是什么——2026-09-18 之前那张表硬编码在 modules/cli/diag.go 里，于是改一个
// 计数器名要同时改两处，而 CLI 那处根本不知道自己在改什么。
func Group(k string) string {
	switch {
	case strings.HasPrefix(k, "requests."):
		return "请求"
	case strings.HasPrefix(k, "chain."):
		return "链"
	case strings.HasPrefix(k, "timeout."):
		return "超时"
	case strings.HasPrefix(k, "client."):
		return "客户端"
	case strings.HasPrefix(k, "breaker."):
		return "熔断"
	case strings.HasPrefix(k, "special."):
		return "插件"
	case strings.HasPrefix(k, "count_tokens."):
		return "兜底"
	}
	return "其他"
}

// Hint 返回计数器名字的人话注释。没列出的不硬凑——空说明比编一句好。
//
// `special.*` 一族由插件自己在注册时声明（special.MetricProvider）：那个名字里
// 带插件名，只有插件知道那一笔业务上意味着什么。
func Hint(k string) string {
	switch {
	case k == "requests.total":
		return "进入网关的请求"
	case k == "count_tokens.forwarded":
		return "转发上游取真值"
	case k == "count_tokens.local":
		return "本地粗估兜底（上游无此端点）"
	case k == "count_tokens.probe_404":
		return "lazy probe 404，记为「上游不支持」"
	case k == "timeout.first_byte.non_stream":
		return "首字节超时（非流式），沿链下移"
	case k == "timeout.first_byte.stream":
		return "首字节超时（流式），沿链下移"
	case strings.HasPrefix(k, "timeout.first_byte"):
		return "首字节超时，沿链下移"
	case k == "chain.failover":
		return "前序候选失败，换到后续候选后成功"
	case k == "chain.step_failed":
		return "链上某站失败（连接 / 可转移错误）"
	case k == "chain.budget_exhausted":
		return "链总预算用尽"
	case k == "client.cancel":
		return "客户端主动取消"
	case k == "breaker.opened":
		return "熔断器打开，provider 暂时摘除"
	case k == "breaker.spared":
		return "形状错误被宽恕，熔断器不予记账"
	case k == "breaker.skipped.shape_error":
		return "请求形状错误（400 被某个形状判据认领，如 deepseek 的 reasoning 回传校验），跳过熔断记账"
	case strings.HasPrefix(k, "special."):
		if hint, ok := special.MetricHint(k); ok {
			return hint
		}
		return "插件改写了请求（逐条有日志）"
	}
	return ""
}
