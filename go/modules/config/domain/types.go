// Package domain 是实体与值类型。**不含任何 IO**。
//
// 谁都可以依赖它，它不依赖任何人（docs/03-architecture.md）。
package domain

import (
	"strings"
	"time"
)

// Roles 语义档位。工具侧只写这些名字，永不写真实模型名。
//
// 四档能力阶梯 + 一个正交的 vision（2026-09-16 从三档扩到四档）：Claude
// 家族自己就是四档，按它对齐，别的家族（只有大中小三个模型）自然映射得下：
//
//	heavy  ← fable   最贵最强，留给明确点名要它的活
//	normal ← opus    **主力**：claude 的 opus 槽、opencode 的 model
//	mid    ← sonnet  分类器 / compact 总结 / subagent
//	light  ← haiku   起标题这类小活
//	vision           多模态，与上面的阶梯正交
//
// 顺序即能力从高到低，别随手调——`newgate status`、doctor、TUI 都按它排。
//
var Roles = []string{"heavy", "normal", "mid", "light", "vision"}

// IsRole 判断一个模型名是不是语义档位名（而不是具体模型名）。
// 代理收到客户端发来的 model 字段时用它区分「档位路由」和「具体模型路由」
// （docs/04-configuration.md）。
func IsRole(s string) bool {
	for _, r := range Roles {
		if r == s {
			return true
		}
	}
	return false
}

const (
	// DefaultPriority profile 没写 priority 时的默认值。取中间值，
	// 用户既能往前插（更小）也能往后放（更大）。
	DefaultPriority = 50
	// ProxyPort 是未显式配置时的本地网关监听端口。
	ProxyPort = 8899
)

// Provider 一个上游账号。
type Provider struct {
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // 优先于 APIKey
	Protocol  string `json:"protocol,omitempty"`    // openai | anthropic，默认 openai

	// AnthropicURL 这个上游的 **Anthropic 方言** base。不填 = 两种方言同一个
	// base（聚合网关都这样）。
	//
	// 为什么需要它：有些上游把两种方言放在不同的 base 上，而且没有互相转发。
	// 火山方舟实测（2026-09-15）：
	//
	//	OpenAI 方言    https://ark.cn-beijing.volces.com/api/coding/v3  /chat/completions
	//	Anthropic 方言 https://ark.cn-beijing.volces.com/api/coding     /v1/messages
	//
	// 前者打后者 404（istio-envoy 路由级 404，空 body），反过来也一样。转发层
	// 是纯字节直通、不做协议转换（docs/01-product.md），客户端发什么方言就发什么方言，
	// 所以「发给哪个 base」只能按**客户端这次说的方言**选——就是这里的用处。
	//
	// 写法跟 ANTHROPIC_BASE_URL 一致（**不含** /v1）：就是你会贴给任何
	// Anthropic 协议客户端工具的那个值，/v1/messages 由 newgate 自己拼。
	// 已经带 /v1 的写法也认（不重复拼）。
	AnthropicURL string `json:"anthropic_url,omitempty"`

	// Models 声明 provider 可提供的具体模型名，供显式模型请求筛选候选；
	// 这类请求只能在提供同名模型的 provider 之间切换，不能改变模型语义。
	Models []string `json:"models,omitempty"`
}

// IsAnthropicPath 这个转发后缀是不是 Anthropic 方言（/messages、
// /messages/count_tokens）。
//
// 判据是**客户端发来的路径**，不是 provider 的 protocol：protocol 说的是
// 「怎么发到上游」（认证方式、走哪个 base），方言说的是「客户端说的是什么」。
// 聚合网关实测两者可以不一致——provider 标 openai，却照样收 /v1/messages。
func IsAnthropicPath(suffix string) bool {
	return suffix == "/messages" || strings.HasPrefix(suffix, "/messages/")
}

// Base 这条后缀该发给哪个上游 base（不带尾部斜杠），不含路径。
func (p Provider) Base(suffix string) string {
	if p.AnthropicURL != "" && IsAnthropicPath(suffix) {
		return strings.TrimRight(p.AnthropicURL, "/")
	}
	return strings.TrimRight(p.BaseURL, "/")
}

// URL 这条后缀的完整上游地址。
//
// 两种方言同 base 时就是 base_url + suffix（聚合网关的老样子）。分开时走
// anthropic_url，并按 ANTHROPIC_BASE_URL 的惯例补上 /v1——方舟那种
// 「…/api/coding」的写法照抄文档就能用，已经带 /v1 的也不重复补。
func (p Provider) URL(suffix string) string {
	if p.AnthropicURL != "" && IsAnthropicPath(suffix) {
		b := p.Base(suffix)
		if !strings.HasSuffix(b, "/v1") {
			b += "/v1"
		}
		return b + suffix
	}
	return strings.TrimRight(p.BaseURL, "/") + suffix
}

// Providers 是 providers.json 的根对象；显式包一层可为格式演进保留空间。
type Providers struct {
	Providers map[string]Provider `json:"providers"`
}

// Binding 一个具体的 (provider, model) 对，或者一条**引用**（Ref 非空）。
type Binding struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Ref 非空 = 这一条不是具体绑定，而是引用另一个键（写作 `@normal` /
	// `{"ref":"normal"}`）：解析时把那个键的链**就地展开**在这一位。
	// 让「一个槽位绑定到某条档位链」和「槽位自己是一条链」用同一套语法。
	// 展开规则见 docs/04-configuration.md、实现见 resolve.BuildChain。
	Ref string `json:"ref,omitempty"`
}