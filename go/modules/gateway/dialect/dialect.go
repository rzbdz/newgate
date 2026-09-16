// Package dialect 记「这个 (provider, model) 听得懂哪些 API 方言」。
//
// 为什么需要它
//
// newgate 的转发层是纯字节直通：客户端发什么方言，就原样发给上游什么
// 方言（claude → anthropic /messages，opencode → openai /chat/completions），
// 不做协议转换。这能成立的前提是**上游听得懂客户端的方言**——聚合器
// （api.rvcompute.com 实测 2026-09）两个方言都收，但 anthropic 方言的
// 私有端点不一定实现：
//
//	POST /v1/messages              → 200（anthropic 方言本体）
//	POST /v1/messages/count_tokens → 404 "Invalid URL"（没实现）
//
// count_tokens 是 Claude Code 的水位条/自动压缩阈值，真值只有上游知道。
// 听得懂就转发拿真值，听不懂退回本地粗估——「听得懂吗」按 (provider,
// model) 记在这里，来源两个，都往同一个注册表里写：
//
//	Mark*()   转发时撞一次 404 / 200，当场学（gate 层面的 probe：
//	          第一个 count_tokens 就是一次探测，代价一个来回）
//	probe 包  newgate probe 主动打一发两种方言 + count_tokens，省掉
//	          那第一次撞墙，也让人在报告里看得见
//
// 只在内存里，和 quirk 同一哲学：进程重启就忘了，重新学的代价是一个
// 请求；要更持久就该落盘，但那是用户的配置目录，不悄悄往里写东西。
// （`newgate probe` 是独立进程，它学到的随进程消失——daemon 自己会在
// 第一个请求上补学，最终状态一致。）
//
// 正反两面都要记：只记「支持」的话，每个「不支持」的上游每次请求都得
// 再撞一次 404。所以 Cap 既是能力位也是已探明位——Known 里为 0 的位
// 表示「探过了，明确不支持」，和「还没探过」区分开。
package dialect

import (
	"sort"
	"strings"
	"sync"
)

// Cap 一种方言能力。位掩码，一个 (provider, model) 可以同时有几个。
type Cap uint32

const (
	// CapOpenAI /chat/completions（openai 方言）可用。
	CapOpenAI Cap = 1 << iota
	// CapAnthropic /messages（anthropic 方言）可用。
	CapAnthropic
	// CapCountTokens anthropic 方言的 /messages/count_tokens 可用。
	CapCountTokens
)

func (c Cap) String() string {
	var parts []string
	if c&CapOpenAI != 0 {
		parts = append(parts, "openai")
	}
	if c&CapAnthropic != 0 {
		parts = append(parts, "anthropic")
	}
	if c&CapCountTokens != 0 {
		parts = append(parts, "count_tokens")
	}
	return strings.Join(parts, "+")
}

type entry struct {
	supported Cap
	known     Cap // known 里为 0 的位 = 探过了、明确不支持
}

var (
	mu   sync.RWMutex
	caps = map[string]entry{}
)

func key(provider, model string) string { return provider + "/" + model }

// Mark 记「支持」。返回 true 表示这是新学到的。
func Mark(provider, model string, c Cap) bool {
	return mark(provider, model, c, true)
}

// MarkUnsupported 记「探过了，明确不支持」。之后 Supports 永远回 false
// ——除非上游哪天升级了（那就得重启 daemon 重新探，成本一个请求）。
func MarkUnsupported(provider, model string, c Cap) bool {
	return mark(provider, model, c, false)
}

func mark(provider, model string, c Cap, ok bool) bool {
	k := key(provider, model)
	mu.Lock()
	defer mu.Unlock()
	e := caps[k]
	if ok {
		// 从「不支持」改口成「支持」也是新信息（上游升级了）
		if e.supported&c == c && e.known&c == c {
			return false
		}
		e.supported |= c
		e.known |= c
		caps[k] = e
		return true
	}
	if e.known&c == c {
		return false
	}
	e.known |= c
	e.supported &^= c
	caps[k] = e
	return true
}

// Supports 查某个能力。known=false 表示还没探过——调用方自己定默认
// 行为（count_tokens 的默认是试一发，见 forward 的懒学习）。
func Supports(provider, model string, c Cap) (ok, known bool) {
	mu.RLock()
	defer mu.RUnlock()
	e := caps[key(provider, model)]
	if e.known&c == 0 {
		return false, false
	}
	return e.supported&c != 0, true
}

// Entry 给 newgate status / probe 报告展示。
type Entry struct {
	Provider string
	Model    string
	Supports Cap
	Known    Cap
}

func Snapshot() []Entry {
	mu.RLock()
	defer mu.RUnlock()
	var out []Entry
	for k, e := range caps {
		i := strings.LastIndexByte(k, '/')
		if i < 0 {
			continue
		}
		out = append(out, Entry{Provider: k[:i], Model: k[i+1:], Supports: e.supported, Known: e.known})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Reset 仅供测试。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	caps = map[string]entry{}
}
