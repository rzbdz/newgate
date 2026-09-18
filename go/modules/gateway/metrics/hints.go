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
//
// **这张表只覆盖数据面自己的计数器。** 别人的计数器由别人自报：调用方先问
// 策略账本（policy.Registry.MetricGroup），没人认领才落到这里。`breaker.*`
// 那三行 2026-09-18 就是这么搬回 modules/breaker 的——熔断器打开意味着什么，
// 不该由这个包解释。
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
	case strings.HasPrefix(k, "special."):
		return "插件"
	case strings.HasPrefix(k, "count_tokens."):
		return "兜底"
	}
	return "其他"
}

// Hint 返回计数器名字的人话注释。没列出的不硬凑——空说明比编一句好。
//
// 同 Group：只有数据面自己的计数器在这里，别人的由贡献者经 MetricNamer 自报。
// `special.*` 一族也是自报的（special.MetricProvider）——那个名字里带插件名，
// 只有插件知道那一笔业务上意味着什么。
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
	case strings.HasPrefix(k, "special."):
		if hint, ok := special.MetricHint(k); ok {
			return hint
		}
		return "插件改写了请求（逐条有日志）"
	}
	return ""
}
