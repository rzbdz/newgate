package breaker

import "bytes"

// IsRequestShapeError 决定这个 4xx/5xx 是否**不该**记进熔断器。
//
// 「请求形状」错误 = 同一个坏请求换一个 provider 也不会变好；记失败只会让熔断
// 器去摘一个本可用的 provider，等客户端重试几次直接拖垮整条链。
//
// 当前只覆盖 reasoning-400（DeepSeek 的「reasoning_content 必须逐字回传」灰度
// 门）。数据面在定案 4xx 分支调用它决定「这次失败要不要记账」，是这条策略的
// **唯一**入口——forward 不该再自己判断。
//
// 下一步（同一次重构的第三刀）：这里的上游专有字符串要搬到 modules/deepseek，
// 改成由上游模块注册的 ShapeDetector。core 只留端口和策略，不留上游的名字。
func IsRequestShapeError(errBody []byte, statusCode int) bool {
	if statusCode != 400 {
		return false
	}
	return isReasoningPassthroughBody(errBody)
}

// isReasoningPassthroughBody 不依赖 forward 包地复用同一个判据：DeepSeek 的 400
// 文案里出现「reasoning_content」（OpenAI 方言）或「content[].thinking」
// （Anthropic 方言）都是这类错误的字段名，两种都要认。
func isReasoningPassthroughBody(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if !bytes.Contains(b, []byte("must be passed back")) &&
		!bytes.Contains(b, []byte("must be passed")) {
		return false
	}
	return bytes.Contains(b, []byte("reasoning_content")) ||
		bytes.Contains(b, []byte("content[].thinking"))
}
