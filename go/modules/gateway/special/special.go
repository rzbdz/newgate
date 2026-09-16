// Package special 是 special_treatment 层：一组可插拔的「特殊照顾」插件。
//
// 为什么需要这一层
//
// 语义命名层的理想是「转发一个请求就该是转发」（docs/01-product.md）。但现实里
// 每个上游都有自己的怪癖，同一个 Anthropic 格式的请求，Claude 官方收下了，
// 某个 DeepSeek 网关就回 400。这类问题有三个共同点：
//
//  1. 只针对**某一个上游**，不能变成全局行为；
//  2. 修法是往请求里补一点东西，而不是改变语义；
//  3. 上游哪天修好了，这段代码就该能干净地摘掉。
//
// 所以它们不该散落在转发热路径的 if 里，而应该是一组注册进来的插件：
// 每个插件自己说明「我认哪个上游」（Match）、「我为什么存在」（Why）、
// 「我改了什么」（Apply 返回的 notes）。热路径只负责按顺序问一遍。
//
// 与 schema 修补的关系：tool schema 修补（rewrite/schema）是**所有**严格
// 校验器都需要的、按 JSON Schema 规范语义无操作的修补，所以它独立成层、
// 默认对所有上游生效。这里放的是「只有某家上游才需要」的补丁。
//
// 三条硬规则
//
//	fail-open：插件报错就当它没跑过，请求按原样发出去。宁可上游报错，
//	           也不能因为一个补丁把整条链弄断（docs/05-gateway.md）。
//	不静默：   改了什么必须回报 notes，由调用方写进日志。用户永远能知道
//	           自己的请求被动过哪一笔。
//	纯字节：   一律用 rewrite 包的字节手术，不做整体 JSON 往返。
package special

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
	"github.com/rzbdz/newgate/go/modules/gateway/rewrite"
)

// Request 是插件能看到的这次转发的上下文。
//
// 故意不含 http.Request：插件只该看请求的**语义归属**（发给谁、什么模型），
// 看不到也改不了头、认证、连接。想动这些的补丁不属于这一层。
//
// 链上只有一份事实源：body 字节。r.Model 是它在每一步之后的**视图**——
// 插件改写了 body 的 model 字段后，由 Apply 框架自动同步（见
// syncContextModel），排在后面的插件立刻看到新值。插件不需要、也不应该
// 手动维护它：忘了维护就是「上游切了模型、下游拿旧模型做决定」的失真
// （2026-09-09 实抓过一次）。其余字段（Provider/Tier/Stream/Agent…）来自
// 路由和请求形态，不来自 body，链上不变。
type Request = gatewayapi.Request

// Plugin 是模块可以挂到请求处理链的 typed capability。
type Plugin = gatewayapi.Plugin

// ToolLoopMigrator 是可选的路由约束：某些上游不能原样接手别家尚未闭合的
// reasoning/tool 状态，但可以在安全候选都失败后做一次显式的有损重建。
// 它属于具体上游的 quirk，不应变成全局 fallback 规则。
type ToolLoopMigrator interface {
	NeedsToolLoopRebase(originProvider, originModel string, candidate *Request) (bool, string)
	RebaseToolLoop(body []byte, candidate *Request) ([]byte, string, error)
}

// RoutePlugin 是 special 层在构链前的扩展点。插件只返回路由意图；如何校验
// binding、构造 fallback 链仍由 resolve 负责。
type RoutePlugin interface {
	Route(body []byte, request *Request, state *domain.State) (RouteDecision, bool)
}

// RouteDecision 是路由插件交回主路由器的声明式意图。
// 插件可以建议链头、档位和首字节超时，但不能自行构造或执行 fallback。
type RouteDecision struct {
	Plugin           string
	Tier             string
	Head             *domain.Binding
	FirstByteTimeout time.Duration
	Note             string
	OverrideNote     string
	OverrideFailNote string
	Metric           string
}

// MetricKey 把插件私有指标规范化到 special 命名空间；缺少任一部分就不发指标。
func (d RouteDecision) MetricKey() string {
	if d.Plugin == "" || d.Metric == "" {
		return ""
	}
	return "special." + d.Plugin + "." + d.Metric
}

// StatusItem 是插件贡献给 `newgate status` 的结构化信息。CLI 只负责排版，
// 不知道 classifier、DeepSeek 等具体机制。
type StatusItem struct {
	Label string
	Value string
}

// StatusProvider 允许插件贡献状态项，而不让 CLI 认识具体插件类型。
type StatusProvider interface {
	Status(state *domain.State) []StatusItem
}

// BindingProvider 让路由插件声明自己可能引入、但不在 profile 中的 binding。
// metrics 用它构造完整观测集合，不需要知道任何插件配置字段。
type BindingProvider interface {
	Bindings(state *domain.State) []domain.Binding
}

// MetricInfo 描述指标对应的动作和排障提示，使计数值保留语义。
type MetricInfo struct {
	Action string
	Hint   string
}

// MetricProvider 是插件可选的指标元数据端口。
type MetricProvider interface {
	Metrics() []MetricInfo
}

// ResponseAuditor 让插件只读检查上游响应，用于发现协议异常而不改写响应。
type ResponseAuditor interface {
	AuditResponse(body []byte) string
}

// Registry 持有当前网关的插件集合、稳定执行顺序和注册所有权 token。
type Registry struct {
	mu      sync.RWMutex
	plugins []Plugin
	tokens  map[string]uint64
	next    uint64
}

var (
	defaultMu       sync.RWMutex
	defaultRegistry = &Registry{}
)

// NewRegistry 创建与其他应用实例隔离的空插件注册表。
func NewRegistry() *Registry { return &Registry{} }

// SetDefault 设置旧调用面使用的注册表；新组件装配应优先使用有所有权的 InstallDefault。
func SetDefault(registry *Registry) {
	if registry == nil {
		panic("special: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

// InstallDefault 安装带所有权的兼容注册表并返回恢复函数。
// 恢复时会核对当前实例，避免旧组件回滚覆盖后来启动的新 owner。
func InstallDefault(registry *Registry) func() {
	if registry == nil {
		panic("special: nil default registry")
	}
	defaultMu.Lock()
	previous := defaultRegistry
	defaultRegistry = registry
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		if defaultRegistry == registry {
			defaultRegistry = previous
		}
		defaultMu.Unlock()
	}
}

func currentRegistry() *Registry {
	defaultMu.RLock()
	registry := defaultRegistry
	defaultMu.RUnlock()
	return registry
}

// Ordered 用插件名声明执行先后约束。这是一张依赖图而不是数字优先级；
// 新增无关插件不会悄悄改变既有插件的相对顺序。
type Ordered interface {
	Before() []string
	After() []string
}

// Register 是兼容调用面；生产装配由 Gateway 组件持有 Registry，并把注册端口
// 注入其他组件。执行顺序由 Ordered 的具名依赖图决定，不依赖加载顺序。
func Register(p Plugin) {
	if _, err := currentRegistry().Register(p); err != nil {
		panic(err)
	}
}

// Register 把插件加入实例注册表，并返回只属于本次注册的撤销句柄。
func (r *Registry) Register(p Plugin) (modules.Release, error) {
	if p == nil || p.Name() == "" {
		return nil, fmt.Errorf("special: plugin name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.plugins {
		if existing.Name() == p.Name() {
			return nil, fmt.Errorf("special: duplicate plugin %s", p.Name())
		}
	}
	next := append(append([]Plugin(nil), r.plugins...), p)
	var orderErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				orderErr = fmt.Errorf("%v", recovered)
			}
		}()
		next = orderPlugins(next)
	}()
	if orderErr != nil {
		return nil, orderErr
	}
	if r.tokens == nil {
		r.tokens = make(map[string]uint64)
	}
	r.next++
	token := r.next
	r.tokens[p.Name()] = token
	r.plugins = next
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[p.Name()] != token {
			return nil
		}
		delete(r.tokens, p.Name())
		for i, existing := range r.plugins {
			if existing.Name() == p.Name() {
				r.plugins = append(r.plugins[:i], r.plugins[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}