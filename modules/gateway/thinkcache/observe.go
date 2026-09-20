package thinkcache

import (
	"bytes"
	"encoding/json"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Observer 旁路观测一条响应，攒出「这一轮的推理内容」和它对应的 key。
//
// 严格只读：喂进来的字节原样是转发给客户端的那一份，Observer 不碰它、
// 也不产生任何回写。转发的字节保真是硬约束（docs/01-product.md），这里只是搭个便车看
// 一眼。看错了最坏的结果是这轮没缓存上，下一轮那条消息补不上原文（插件
// 不编内容，见 modules/deepseek/st-reasoning.go 文件头）。
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

	// ---- 计数（2026-09-18 加，只为日志取证）----
	//
	// 存在的理由：线上出现「上游没给推理内容」时，只看结果（推理字节数 0）
	// 分不清是我们**没解析出来**还是上游**真没给**。用户的原话是「你他妈给我
	// trace 出来」。这几个计数就是那份 trace：哪一方言、收了多少块、其中
	// 多少个推理增量 / 正文增量、几个块连 JSON 都解析不了。
	chunks       int
	badChunks    int
	reasonDeltas int
	textDeltas   int
	sawOpenAI    bool
	sawAnthropic bool
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
		o.badChunks++
		return // 不认识的形状：跳过，不是错
	}
	o.chunks++

	for _, ch := range c.Choices { // OpenAI 方言
		d := ch.Delta
		o.sawOpenAI = true
		if d.ReasoningContent != "" {
			o.reasonDeltas++
			o.reason.WriteString(d.ReasoningContent)
		} else if d.Reasoning != "" {
			o.reasonDeltas++
			o.reason.WriteString(d.Reasoning)
		}
		if d.Content != "" {
			o.textDeltas++
			o.text.WriteString(d.Content)
		}
		for _, tc := range d.ToolCalls {
			o.addTool(tc.ID)
		}
	}

	switch c.Type { // Anthropic 方言
	case "content_block_start":
		o.sawAnthropic = true
		if c.ContentBlock.Type == "tool_use" {
			o.addTool(c.ContentBlock.ID)
		}
		if c.ContentBlock.Thinking != "" {
			o.reasonDeltas++
			o.reason.WriteString(c.ContentBlock.Thinking)
		}
		if c.ContentBlock.Text != "" {
			o.textDeltas++
			o.text.WriteString(c.ContentBlock.Text)
		}
	case "content_block_delta":
		o.sawAnthropic = true
		switch c.Delta.Type {
		case "thinking_delta":
			o.reasonDeltas++
			o.reason.WriteString(c.Delta.Thinking)
		case "text_delta":
			o.textDeltas++
			o.text.WriteString(c.Delta.Text)
		}
	}
}

func (o *Observer) addTool(id string) {
	if id == "" || o.seenID[id] {
		return
	}
	o.seenID[id] = true
	o.toolIDs = append(o.toolIDs, id)
}

// ObserveBody 非流式响应：整个 body 一次看完。
func (o *Observer) ObserveBody(body []byte) {
	var r struct {
		// OpenAI
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		// Anthropic
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
			ID       string `json:"id"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &r) != nil {
		return
	}
	for _, ch := range r.Choices {
		m := ch.Message
		if m.ReasoningContent != "" {
			o.reason.WriteString(m.ReasoningContent)
		} else if m.Reasoning != "" {
			o.reason.WriteString(m.Reasoning)
		}
		o.text.WriteString(m.Content)
		for _, tc := range m.ToolCalls {
			o.addTool(tc.ID)
		}
	}
	for _, b := range r.Content {
		switch b.Type {
		case "thinking":
			o.reason.WriteString(b.Thinking)
		case "text":
			o.text.WriteString(b.Text)
		case "tool_use":
			o.addTool(b.ID)
		}
	}
}

// Wire 汇报这条响应**线路层**看到了什么，供日志取证。
//
// 一行说清「上游到底发了什么」：哪一方言、多少块、其中多少个推理增量。
// 与 ReasoningBytes 配对看就能立刻分辨两件事：
//
//	Wire: 200 块 / 推理增量 0 / 正文增量 40   ⇒ 上游真的没发推理（请求侧的事）
//	Wire: 0 块（全是坏块）/ …                  ⇒ 我们没解析出来（网关侧的事）
//
// 不打任何内容，只有计数和布尔。
func (o *Observer) Wire() string {
	if o == nil {
		return i18n.T("(no observer)", nil)
	}
	dialect := i18n.T("unknown dialect", nil)
	switch {
	case o.sawAnthropic && o.sawOpenAI:
		dialect = i18n.T("mixed dialects", nil)
	case o.sawAnthropic:
		dialect = "anthropic"
	case o.sawOpenAI:
		dialect = "openai"
	}
	if o.badChunks > 0 {
		return i18n.T("{dialect} {chunks} chunks / {reasoning} reasoning deltas / {text} text deltas / **{bad} chunks unparsable**",
			i18n.A{"dialect": dialect, "chunks": o.chunks, "reasoning": o.reasonDeltas,
				"text": o.textDeltas, "bad": o.badChunks})
	}
	return i18n.T("{dialect} {chunks} chunks / {reasoning} reasoning deltas / {text} text deltas",
		i18n.A{"dialect": dialect, "chunks": o.chunks, "reasoning": o.reasonDeltas,
			"text": o.textDeltas})
}

// Keys 这一轮的推理内容该挂在哪些 key 上。
func (o *Observer) Keys() []string {
	var keys []string
	for _, id := range o.toolIDs {
		keys = append(keys, ToolKey(id))
	}
	if k := TextKey(o.text.String()); k != "" {
		keys = append(keys, k)
	}
	return keys
}

// Commit 入库。返回缓存了多少字节、挂了几个 key；没看到推理内容就是 (0,0)。
// 调用方拿这两个数字写日志——**不要打内容**。
func (o *Observer) Commit(c *Cache) (nbytes, nkeys int) {
	return o.CommitWithOrigin(c, Origin{})
}

// CommitWithOrigin 除推理原文外，还记录 tool call 的实际产生上游。即使这一轮
// 没有 reasoning，也要记 origin：tool loop 的方言状态仍不能安全跨 provider。
func (o *Observer) CommitWithOrigin(c *Cache, origin Origin) (nbytes, nkeys int) {
	if o == nil || c == nil {
		return 0, 0
	}
	c.PutOrigin(o.toolIDs, origin)
	if o.reason.Len() == 0 {
		return 0, 0
	}
	keys := o.Keys()
	if len(keys) == 0 {
		return 0, 0 // 没有能对上号的 key，存了也找不回来
	}
	c.Put(keys, o.reason.Bytes())
	return o.reason.Len(), len(keys)
}

// Reasoning 观测到的推理内容（测试与非流式路径用）。
func (o *Observer) Reasoning() string { return o.reason.String() }

// ReasoningBytes 观测到的推理内容长度，不产生拷贝。
//
// 为什么要有它而不是 len(Reasoning())：调用点在转发循环里、每一条响应都跑，
// 而推理内容动辄上百 KB——为了写一行日志把整段复制成 string 是白花的。
func (o *Observer) ReasoningBytes() int {
	if o == nil {
		return 0
	}
	return o.reason.Len()
}

// ToolCalls 这一轮观测到的 tool call 个数（去重后）。
//
// 给日志用：上游一轮「有 tool call 却一个字节推理都没给」时，下一轮的补丁
// 对这条消息**一条都补不上**（插件不编占位符），而**那是上游本来就没给，
// 不是我们弄丢的**——这两件事的处置完全相反，日志必须能分开（见
// modules/deepseek/st-reasoning.go 的 skipCause）。
func (o *Observer) ToolCalls() int {
	if o == nil {
		return 0
	}
	return len(o.toolIDs)
}

// LooksLikeSSE 粗判一个 Content-Type 是不是事件流。
func LooksLikeSSE(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "event-stream")
}
