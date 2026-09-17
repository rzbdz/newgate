package breaker

import "bytes"

// shapeOf 问一句「这次失败是不是请求形状问题」。
//
// 形状错误的定义与检测都属于**上游自己**：同一份请求 body 换一个 provider
// 也不会变好，所以它既不该记在这家头上，也确实值得换个校验更松的 provider
// 再试一次。core 只提供这个端口和「永不摘牌，只计数」的策略。
//
// 现在这里还硬编码着 DeepSeek 那条规则；紧接着的一刀会把它搬到
// modules/deepseek，改成由上游模块注册的 ShapeDetector，core 里不留任何
// 上游专有字符串。
func (b *table) shapeOf(status int, body []byte) bool {
	return IsRequestShapeError(body, status)
}

// IsRequestShapeError 决定这个 4xx/5xx 是否**不该**记进可用性账本。
//
// 当前只覆盖 reasoning-400（DeepSeek 的「reasoning_content 必须逐字回传」灰度
// 门）。数据面用它决定要不要打 [reasoning-400] 那条专属日志并单独存档证据；
// 「折不折账」由 Classify 的 Bucket 决定，两者不再混在一起。
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
