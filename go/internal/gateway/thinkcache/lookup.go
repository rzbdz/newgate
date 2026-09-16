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

// ContinuationOrigin 判断请求是否正处于未闭合的 tool loop，并找出 tool call
// 的产生上游。只看请求尾部：普通 user 新回合即使历史里有 tool calls，也不应
// 被永久粘住。
//
// 两种方言：
//   - Anthropic：最后一条 user.content[] 含 tool_result
//   - OpenAI：请求末尾连续的 role=tool 消息
func (c *Cache) ContinuationOrigin(body []byte) (Origin, bool) {
	var req struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 {
		return Origin{}, false
	}

	var ids []string
	last := req.Messages[len(req.Messages)-1]
	switch last.Role {
	case "tool": // OpenAI 方言；一次并行调用可能有多条连续 tool 消息
		for i := len(req.Messages) - 1; i >= 0 && req.Messages[i].Role == "tool"; i-- {
			if id := req.Messages[i].ToolCallID; id != "" {
				ids = append(ids, id)
			}
		}
	case "user": // Anthropic 方言；多个 tool_result 在同一个 content[] 里
		if len(last.Content) == 0 || last.Content[0] != '[' {
			return Origin{}, false
		}
		var blocks []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
		}
		if json.Unmarshal(last.Content, &blocks) != nil {
			return Origin{}, false
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				ids = append(ids, b.ToolUseID)
			}
		}
	default:
		return Origin{}, false
	}

	var found Origin
	for _, id := range ids {
		origin, ok := c.GetOrigin(id)
		if !ok {
			continue
		}
		if found.Provider == "" {
			found = origin
			continue
		}
		if found.Provider != origin.Provider || found.Model != origin.Model {
			return Origin{}, false // 同一批 tool results 来源冲突：不猜
		}
	}
	return found, found.Provider != ""
}
