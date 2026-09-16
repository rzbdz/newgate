package thinkcache

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Observer 旁路观测一条响应，攒出「这一轮的推理内容」和它对应的 key。
//
// 严格只读：喂进来的字节原样是转发给客户端的那一份，Observer 不碰它、
// 也不产生任何回写。转发的字节保真是硬约束（docs/01-product.md），这里只是搭个便车看
// 一眼。看错了最坏的结果是这轮没缓存上，退回占位符。
//
// 用法：
//
//	ob := thinkcache.NewObserver()
//	… 每次 Read 到就 ob.Write(buf[:n]) …
//	ob.Commit(thinkcache.Default)   // 流结束时
type Observer struct {
	pending bytes.Buffer // SSE 还没凑齐一个事件的尾巴
	reason  bytes.Buffer // 累积的推理内容
	text    bytes.Buffer // 累积的正文（纯文本轮的 key 靠它）
	toolIDs []string
	seenID  map[string]bool
}

func NewObserver() *Observer {
	return &Observer{seenID: map[string]bool{}}
}

// Write 喂原始响应字节。调用方可以任意分块——SSE 的事件边界由这里自己找。
func (o *Observer) Write(p []byte) {
	if o == nil {
		return
	}
	// 上限保护：一条流的推理内容再长也不该吃掉几百 MB。
	// 超了就停止累积——宁可这轮没缓存上，也不能把网关吃爆。
	if o.reason.Len() > 8<<20 {
		return
	}
	o.pending.Write(p)
	for {
		s := o.pending.Bytes()
		i := bytes.Index(s, []byte("\n\n"))
		if i < 0 {
			// 半个事件也可能很长（一个 chunk 里塞了大段推理），
			// 但没有边界就不能解析。给个上限防止无限攒。
			if o.pending.Len() > 4<<20 {
				o.pending.Reset()
			}
			return
		}
		event := make([]byte, i)
		copy(event, s[:i])
		o.pending.Next(i + 2)
		o.feedEvent(event)
	}
}

func (o *Observer) feedEvent(event []byte) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue // event: / id: / 注释行，与我们无关
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		o.feedChunk(payload)
	}
}

// sseChunk 同时容纳两种方言。用 json.Unmarshal 是安全的：这里只读、不回写，
// 不存在「往返把请求改坏」的问题（那条规矩管的是转发的字节）。
type sseChunk struct {
	// OpenAI 方言
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"` // 部分聚合网关用这个名字
			ToolCalls        []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`

	// Anthropic 方言
	Type  string `json:"type"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
	ContentBlock struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Thinking string `json:"thinking"`
		Text     string `json:"text"`
	} `json:"content_block"`
}

func (o *Observer) feedChunk(payload []byte) {
	var c sseChunk
	if json.Unmarshal(payload, &c) != nil {
		return // 不认识的形状：跳过，不是错
	}

	for _, ch := range c.Choices { // OpenAI 方言
		d := ch.Delta
		if d.ReasoningContent != "" {
			o.reason.WriteString(d.ReasoningContent)
		} else if d.Reasoning != "" {
			o.reason.WriteString(d.Reasoning)
		}
		if d.Content != "" {
			o.text.WriteString(d.Content)
		}
		for _, tc := range d.ToolCalls {
			o.addTool(tc.ID)
		}
	}

	switch c.Type { // Anthropic 方言
	case "content_block_start":
		if c.ContentBlock.Type == "tool_use" {
			o.addTool(c.ContentBlock.ID)
		}
		o.reason.WriteString(c.ContentBlock.Thinking)
		o.text.WriteString(c.ContentBlock.Text)
	case "content_block_delta":
		switch c.Delta.Type {
		case "thinking_delta":
			o.reason.WriteString(c.Delta.Thinking)
		case "text_delta":
			o.text.WriteString(c.Delta.Text)
		}
	}
}