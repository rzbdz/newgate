package thinking

import (
	"github.com/rzbdz/newgate/modules/gateway/quirk"
)

// signatures 是「上游说它不支持关闭思考」的**报错原文**，按各家方言分组。
//
// # 为什么它们住在这里，而不是 gateway/quirk
//
// 这三条文本分别是智谱 GLM、DeepSeek、Kimi 的方言，而**补丁**（把
// `thinking:{"type":"disabled"}` 翻成 `enabled` + `reasoning_effort:low`，见
// st-always.go）也住在本模块。按仓库的规矩（modules/deepseek/shape.go 写得很
// 清楚）：「判据待在知道真相的模块里，它才能被这个模块自己测试、自己演进，而
// core 里不再出现任何上游专有字符串」。
//
// 它们原来硬编码在 `gateway/quirk` 里，于是加一家新上游的唯一办法是改 gateway。
// 代价已经付过一次：kimi 那句措辞在补进来之前「一条签名都不匹配，quirk 永远学
// 不到它」，每一次后台调用都重新撞一遍 400；而 GLM 那边因为文案是中文、早几天
// 就学会了。同一个坑、同一个补丁，只因为文本住错了地方就漏了一个上游。
//
// # 加新一条的门槛
//
// 必须是**实测复现过**的报错原文（把 dump 文件名写进注释），而且补丁语义上说得
// 过去。猜的规则会让我们给一堆无关请求乱加字段，比不修更糟。
func signatures() []quirk.Signature {
	return []quirk.Signature{{
		Flag: quirk.NoThinkingDisable,
		Any: []string{
			// 智谱 GLM，code 1210。
			"不支持关闭思考",
			"始终思考",               // 同上，措辞变体
			"cannot be disabled", // deepseek: thinking options type cannot be disabled…
			"does not support disabling thinking",
			"thinking cannot be turned off",
			// kimi：现场 dump/err-400-req000213、req000230（route:
			// mid -> kimi/kimi-k2.7-code，Claude Code 的后台调用，thinking 字段
			// 本来没有，claudecode 的后台插件替它补了 thinking:{"type":"disabled"}，
			// 上游回这句）。
			"only type=enabled is allowed",
		},
		Label: "该模型始终思考",
	}}
}
