// Package quirk 记「这个上游有什么毛病」，从它自己的报错现场学。
//
// 为什么需要它
//
// special_treatment 的插件靠 Match 认领请求，而 Match 只能看模型名、provider
// 名、URL 这些**静态**信息。有些毛病是静态看不出来的：
//
//	[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。]
//
// 这句话是 glm-5.3 在**带 tools** 的请求上回的。实测（api.rvcompute.com 聚合
// 器 + glm-5.3）：
//
//	tools + 不给 reasoning_effort        → 400 / 1210
//	tools + reasoning_effort=low|high    → 200
//	同样带 tools 的 glm-5.2 / glm-4.7    → 200
//
// 成因基本可以确定：聚合器看见 tools 就自己给上游塞了「关闭思考」（很多模型
// 不支持思考+工具同时用），而 glm-5.3 是始终思考的模型，于是拒掉。我们改不了
// 聚合器，但给一个**显式**的 reasoning_effort 就能盖过它塞的那个值。
//
// 光看模型名猜不出来哪个模型「始终思考」——那是模型版本的属性，会变，也没有
// 任何接口能查。所以这里的策略是：**认签名、记下来、下次带上补丁**。
// 来源有两个，都往同一个注册表里写：
//
//	Learn()  转发时撞上 4xx，认出签名就记一笔（一次失败换永久免疫）
//	Probe()  newgate probe 主动打一发探出来，省掉那一次失败
//
// 只在内存里。进程重启就忘了，重新学一次的代价是一个请求。要它更持久就该
// 落到 providers.json 里，但那是用户的配置文件，我们不该悄悄往里写东西。
package quirk

import (
	"strings"
	"sync"
)

// Flag 一个已知毛病。用位掩码，一个 (provider, model) 可以同时有好几个。
type Flag uint32

const (
	// NoThinkingDisable 这个模型不接受「关闭思考」，必须给显式的思考强度。
	NoThinkingDisable Flag = 1 << iota
)

func (f Flag) String() string {
	if f&NoThinkingDisable != 0 {
		return "不支持关闭思考（必须给显式 reasoning_effort）"
	}
	return "未知"
}

var (
	mu    sync.RWMutex
	flags = map[string]Flag{}
)

func key(provider, model string) string { return provider + "/" + model }

// Mark 记一笔。返回 true 表示这是**新学到的**——调用方据此决定要不要打日志，
// 免得同一件事每个请求都刷一行。
func Mark(provider, model string, f Flag) bool {
	k := key(provider, model)
	mu.Lock()
	defer mu.Unlock()
	if flags[k]&f == f {
		return false
	}
	flags[k] |= f
	return true
}

// Has 查这个 (provider, model) 有没有某个毛病。
func Has(provider, model string, f Flag) bool {
	mu.RLock()
	defer mu.RUnlock()
	return flags[key(provider, model)]&f != 0
}

// Entry 给 newgate status / doctor 展示。
type Entry struct {
	Provider string
	Model    string
	Flags    Flag
}

func Snapshot() []Entry {
	mu.RLock()
	defer mu.RUnlock()
	var out []Entry
	for k, f := range flags {
		i := strings.LastIndexByte(k, '/')
		if i < 0 {
			continue
		}
		out = append(out, Entry{Provider: k[:i], Model: k[i+1:], Flags: f})
	}
	return out
}

// Reset 仅供测试。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	flags = map[string]Flag{}
}

// signature 一条「报错长这样 → 说明有这个毛病」的规则。
//
// 加新规则的门槛：必须是**实测复现过**的报错原文，而且补丁得是语义上说得过去
// 的。猜的规则会让我们给一堆无关请求乱加字段，比不修更糟。
type signature struct {
	flag  Flag
	any   []string // 报错原文里出现任意一条即命中（小写比较）
	label string
}

var signatures = []signature{{
	flag: NoThinkingDisable,
	any: []string{
		"不支持关闭思考",           // 智谱 GLM，code 1210
		"始终思考",              // 同上，措辞变体
		"cannot be disabled",  // deepseek: thinking options type cannot be disabled…
		"does not support disabling thinking",
		"thinking cannot be turned off",
	},
	label: "该模型始终思考",
}}

// Learn 从一次失败的转发里学。只看 4xx——5xx 是上游自己挂了，跟请求形状无关。
//
// 返回学到的东西（人话，可以直接写日志）；什么都没学到就返回 nil。
// 认不出的报错一律不猜：宁可让用户看到原始报错，也不能瞎加字段。
func Learn(provider, model string, status int, body []byte) []string {
	if status < 400 || status >= 500 || len(body) == 0 {
		return nil
	}
	low := strings.ToLower(string(body))
	var learned []string
	for _, sg := range signatures {
		hit := false
		for _, pat := range sg.any {
			if strings.Contains(low, strings.ToLower(pat)) {
				hit = true
				break
			}
		}
		if hit && Mark(provider, model, sg.flag) {
			learned = append(learned, sg.label)
		}
	}
	return learned
}
