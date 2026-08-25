// Package thinkcache 帮客户端兜住它自己丢掉的推理内容。
//
// 为什么必须有这一层
//
// DeepSeek 的思考模式把推理内容当**对话状态**：请求一旦带了 tools，
// 历史里每条 assistant 消息都必须把当轮的推理内容原样带回来，哪怕那一轮
// 没做 tool call，否则 400。而且文档写明**必须逐字**——截断或摘要在某些
// 版本上同样被拒。
//
// 问题是客户端做不到。Claude Code 这类客户端对非官方端点会主动剥掉
// thinking 块（它假定只有官方签名的块能回传），opencode 那边则是把
// assistant 消息序列化进上下文时丢掉了 reasoning_content 这个它不认识的
// 字段。两边都不是配置能改的。
//
// 补空串能骗过「字段在不在」的检查，但那是**更坏**的结果：模型下一轮读到
// 自己上一轮「什么都没想」，推理质量直接塌掉。所以唯一正确的做法是网关
// 自己把上游吐出来的推理内容记住，下一轮原样插回去。
//
// 边界
//
//	只在内存里。推理内容就是对话内容，落盘是隐私决策，不该由我们静默替
//	用户做。进程重启就没了，那时退回补空串——不 400，但那一轮的推理确实
//	丢了，调用方必须在日志里说清楚，不能装作补上了。
//
//	不打日志。任何路径都不打内容本身，只打字节数和 key 的前缀。
//
//	有上限。按总字节封顶 + TTL 双重淘汰，绝不让它无限长。
package thinkcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Cache 一个按字节数封顶、带 TTL 的 LRU。
type Cache struct {
	mu       sync.Mutex
	ll       *list.List // 队头 = 最近用过
	items    map[string]*list.Element
	bytes    int64
	maxBytes int64
	ttl      time.Duration

	hits, misses, puts, evictions uint64
}

type entry struct {
	key  string
	blob []byte
	at   time.Time
}

// Default 全局实例。32MB / 2 小时：一次长会话的推理内容量级是几百 KB，
// 32MB 够放几十个并行会话；TTL 只是为了让忘掉的会话自己腾地方。
var Default = New(32<<20, 2*time.Hour)

func New(maxBytes int64, ttl time.Duration) *Cache {
	return &Cache{
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		maxBytes: maxBytes,
		ttl:      ttl,
	}
}

// Put 把一段推理内容挂到若干个 key 上。
//
// 一轮回答可能同时给出多个 tool_call，客户端下一轮回传时我们只要认出**任
// 意一个** id 就能找回这段推理，所以同一段内容挂多个 key。多挂的 key 各自
// 计一次字节数（宁可提前淘汰，也不做复杂的引用计数）。
func (c *Cache) Put(keys []string, blob []byte) {
	if len(keys) == 0 || len(blob) == 0 {
		return
	}
	// 拷一份：调用方的 buffer 会被复用
	cp := make([]byte, len(blob))
	copy(cp, blob)

	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if el, ok := c.items[k]; ok {
			e := el.Value.(*entry)
			c.bytes += int64(len(cp)) - int64(len(e.blob))
			e.blob, e.at = cp, now
			c.ll.MoveToFront(el)
			continue
		}
		el := c.ll.PushFront(&entry{key: k, blob: cp, at: now})
		c.items[k] = el
		c.bytes += int64(len(cp))
	}
	c.puts++
	c.evictLocked(now)
}

// Get 取回推理内容。过期的当没有。
func (c *Cache) Get(key string) ([]byte, bool) {
	if key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}
	e := el.Value.(*entry)
	if c.ttl > 0 && time.Since(e.at) > c.ttl {
		c.removeLocked(el)
		c.misses++
		return nil, false
	}
	c.ll.MoveToFront(el)
	c.hits++
	return e.blob, true
}

func (c *Cache) evictLocked(now time.Time) {
	// 先清过期的（从队尾开始，那边最旧）
	if c.ttl > 0 {
		for el := c.ll.Back(); el != nil; {
			prev := el.Prev()
			if now.Sub(el.Value.(*entry).at) <= c.ttl {
				break
			}
			c.removeLocked(el)
			c.evictions++
			el = prev
		}
	}
	for c.bytes > c.maxBytes {
		el := c.ll.Back()
		if el == nil {
			return
		}
		c.removeLocked(el)
		c.evictions++
	}
}

func (c *Cache) removeLocked(el *list.Element) {
	e := el.Value.(*entry)
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.bytes -= int64(len(e.blob))
}

// Stats 给 newgate status / st 展示。不含内容。
type Stats struct {
	Entries   int
	Bytes     int64
	MaxBytes  int64
	Hits      uint64
	Misses    uint64
	Puts      uint64
	Evictions uint64
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Entries: c.ll.Len(), Bytes: c.bytes, MaxBytes: c.maxBytes,
		Hits: c.hits, Misses: c.misses, Puts: c.puts, Evictions: c.evictions,
	}
}

// ToolKey 按 tool_call id 做 key。
//
// 这是最稳的 key：id 由上游生成，客户端**必须**逐字回传（tool_result 要靠
// 它对上号），所以它在两次请求里必然一致。而「请求带了 tools」正是上游要求
// 回传推理内容的唯一场景，两者刚好重合。
func ToolKey(id string) string {
	if id == "" {
		return ""
	}
	return "tool:" + id
}

// TextKey 按 assistant 正文的哈希做 key，用于**没有** tool_call 的那些轮。
//
// 文档说得很明确：一旦请求带了 tools，历史里每条 assistant 消息都要带推理
// 内容，哪怕那一轮没做 tool call。所以纯文本轮也得能找回来，而它身上唯一
// 会被客户端原样带回的东西就是正文。
//
// 弱点：客户端要是改写过正文（截断、加前缀）就 miss。接受它——主路径
// （带 tool_call 的轮）走 ToolKey，那条是稳的。
func TextKey(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	h := sha256.Sum256([]byte(t))
	return "text:" + hex.EncodeToString(h[:12])
}
