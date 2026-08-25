package thinkcache

import "encoding/json"

// KeysForAssistantMessage 从**请求里**一条 assistant 消息算出候选 key，
// 顺序即优先级。这是 Observer.Keys() 的镜像：一边在响应里挂 key，一边在
// 下一轮的请求里按同样的规则找回来，两边必须严格对应，改一边就得改另一边。
//
// 两种方言都认（newgate 不做协议转换，客户端发什么方言就原样转发什么方言，
// 所以这里必须两边都能认）：
//
//	OpenAI    {"role":"assistant","content":"…","tool_calls":[{"id":"…"}]}
//	Anthropic {"role":"assistant","content":[{"type":"tool_use","id":"…"},…]}
func KeysForAssistantMessage(item []byte) []string {
	var m struct {
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(item, &m) != nil {
		return nil
	}

	var keys []string
	for _, tc := range m.ToolCalls { // OpenAI 方言
		if k := ToolKey(tc.ID); k != "" {
			keys = append(keys, k)
		}
	}

	text := ""
	if len(m.Content) > 0 {
		switch m.Content[0] {
		case '"': // content 是字符串
			_ = json.Unmarshal(m.Content, &text)
		case '[': // content 是块数组（Anthropic 方言）
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				ID   string `json:"id"`
			}
			if json.Unmarshal(m.Content, &blocks) == nil {
				for _, b := range blocks {
					switch b.Type {
					case "tool_use":
						if k := ToolKey(b.ID); k != "" {
							keys = append(keys, k)
						}
					case "text":
						text += b.Text
					}
				}
			}
		}
	}
	if k := TextKey(text); k != "" {
		keys = append(keys, k)
	}
	return keys
}

// Lookup 按候选 key 依次查，命中即返回。
func (c *Cache) Lookup(item []byte) ([]byte, bool) {
	for _, k := range KeysForAssistantMessage(item) {
		if blob, ok := c.Get(k); ok {
			return blob, true
		}
	}
	return nil, false
}
