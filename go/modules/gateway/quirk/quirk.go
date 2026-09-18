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

// Table 是这张「上游毛病」表。
//
// **它是一个实例，不是包级变量**（2026-09-18 改）：包级 map 是典型的 service
// locator——别的模块（thinking 的 st-always）直接调 `quirk.Has(...)` 就依赖上了
// gateway 数据面写进去的状态，而那条依赖在 `Requires`、依赖图、棘轮测试里都看
// 不见。docs/03-architecture.md §5 明写「不得新增 service locator」，本仓库也为
// breaker 修过一模一样的问题（见 modules/breaker/shape.go 的说明：健康表做成
// table 实例，就是因为它要在测试里起很多份而不互相污染）。
//
// 现在由 gateway 持有唯一的 Default，并**随每次请求交给插件**（见
// modules/gateway/special 的 `Request.Quirks`）——想要它的模块读的是手里那个
// 请求上的字段，也就是网关自己交出来的东西，而不是一个它可以绕过去的全局。
type Table struct {
	mu    sync.RWMutex
	flags map[string]Flag
}

// Default 是 gateway 自己的那一份（probe / forward / Request 的填充都用它）。
//
// **它是 gateway 的内部约定，不是给别的模块用的**。别的模块要读这张表，走
// `special.Request.Quirks`（网关在每次请求上带过去的那一个）——那是一条**声明过**
// 的依赖（消费方必然已经 Need 了 gateway 的端口），而这里写 `quirk.Default.Has(...)`
// 会重新引入一条依赖图上看不见的 service locator。
//
// 为什么不把它设成不导出：probe 与 forward 是本模块的**兄弟包**，它们要的是同一
// 份状态，而 Go 的 internal/ 会把 gopher 挡在外面却也挡住它们命名这个类型；真正
// 要防的是「别的模块直接调」，那一条由 `special.Request.Quirks` 这条正路 + 下面
// 这段注释管着。
var Default = NewTable()

// NewTable 建一张空表（测试里要隔离时用）。
func NewTable() *Table { return &Table{flags: map[string]Flag{}} }

func key(provider, model string) string { return provider + "/" + model }

// Mark 记一笔。返回 true 表示这是**新学到的**——调用方据此决定要不要打日志，
// 免得同一件事每个请求都刷一行。
func (t *Table) Mark(provider, model string, f Flag) bool {
	k := key(provider, model)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.flags[k]&f == f {
		return false
	}
	t.flags[k] |= f
	return true
}

// Has 查这个 (provider, model) 有没有某个毛病。
func (t *Table) Has(provider, model string, f Flag) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.flags[key(provider, model)]&f != 0
}

// Entry 给 newgate status / doctor 展示。
type Entry struct {
	Provider string
	Model    string
	Flags    Flag
}

func (t *Table) Snapshot() []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Entry
	for k, f := range t.flags {
		i := strings.LastIndexByte(k, '/')
		if i < 0 {
			continue
		}
		out = append(out, Entry{Provider: k[:i], Model: k[i+1:], Flags: f})
	}
	return out
}

// Reset 清空（测试隔离用；生产代码不该调它——这张表是「一次失败换永久免疫」，
// 跨装配保留是**预期语义**，不是污染）。
func (t *Table) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flags = map[string]Flag{}
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
		"不支持关闭思考",            // 智谱 GLM，code 1210
		"始终思考",               // 同上，措辞变体
		"cannot be disabled", // deepseek: thinking options type cannot be disabled…
		"does not support disabling thinking",
		"thinking cannot be turned off",
		// 2026-09-17 补：kimi。现场 dump/err-400-req000213、req000230
		// （route: mid -> kimi/kimi-k2.7-code，Claude Code 的后台调用，
		// thinking 字段本来没有，claudecode 的后台插件替它补了
		// thinking:{"type":"disabled"}，上游回这句）。
		//
		// 加之前这句措辞**一条签名都不匹配**，所以 quirk 永远学不到它：
		// 每一次后台调用都重新撞一遍 400。而这条路本来是和 GLM 1210 同一
		// 个坑、同一个补丁（disabled → enabled + reasoning_effort:low），
		// GLM 那边 2026-09-10 就学会了（日志 `#82 学到：smt-glm/glm-5.3-flash
		// 该模型始终思考`），kimi 这边因为文案不同一直漏着。
		"only type=enabled is allowed",
	},
	label: "该模型始终思考",
}}

// Learn 从一次失败的转发里学。只看 4xx——5xx 是上游自己挂了，跟请求形状无关。
//
// 返回学到的东西（人话，可以直接写日志）；什么都没学到就返回 nil。
// 认不出的报错一律不猜：宁可让用户看到原始报错，也不能瞎加字段。
func (t *Table) Learn(provider, model string, status int, body []byte) []string {
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
		if hit && t.Mark(provider, model, sg.flag) {
			learned = append(learned, sg.label)
		}
	}
	return learned
}
