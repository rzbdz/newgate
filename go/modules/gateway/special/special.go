// Package special 是 special_treatment 层：一组可插拔的「特殊照顾」插件。
//
// 为什么需要这一层
//
// 语义命名层的理想是「转发一个请求就该是转发」（docs/16）。但现实里
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
//	           也不能因为一个补丁把整条链弄断（docs/16 §6.1）。
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

type StatusProvider interface {
	Status(state *domain.State) []StatusItem
}

// BindingProvider 让路由插件声明自己可能引入、但不在 profile 中的 binding。
// metrics 用它构造完整观测集合，不需要知道任何插件配置字段。
type BindingProvider interface {
	Bindings(state *domain.State) []domain.Binding
}

type MetricInfo struct {
	Action string
	Hint   string
}

type MetricProvider interface {
	Metrics() []MetricInfo
}

type ResponseAuditor interface {
	AuditResponse(body []byte) string
}

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

func NewRegistry() *Registry { return &Registry{} }

func SetDefault(registry *Registry) {
	if registry == nil {
		panic("special: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

// InstallDefault installs an owned compatibility registry and returns an
// ownership-aware restore function. A stale component cannot overwrite a
// newer owner's registry during rollback.
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

// Ordered declares named execution constraints. This is a dependency graph,
// not a numeric priority: adding an unrelated plugin cannot silently change
// the relative order of existing modules.
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

func orderPlugins(plugins []Plugin) []Plugin {
	byName := make(map[string]Plugin, len(plugins))
	index := make(map[string]int, len(plugins))
	edges := make(map[string]map[string]bool, len(plugins))
	indegree := make(map[string]int, len(plugins))
	for i, plugin := range plugins {
		byName[plugin.Name()] = plugin
		index[plugin.Name()] = i
		edges[plugin.Name()] = map[string]bool{}
	}
	addEdge := func(from, to string) {
		if _, ok := byName[from]; !ok {
			return
		}
		if _, ok := byName[to]; !ok || edges[from][to] {
			return
		}
		edges[from][to] = true
		indegree[to]++
	}
	for _, plugin := range plugins {
		ordered, ok := plugin.(Ordered)
		if !ok {
			continue
		}
		for _, before := range ordered.Before() {
			addEdge(plugin.Name(), before)
		}
		for _, after := range ordered.After() {
			addEdge(after, plugin.Name())
		}
	}

	var ready []string
	for name := range byName {
		if indegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
	out := make([]Plugin, 0, len(plugins))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		out = append(out, byName[name])
		for next := range edges[name] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
				sort.Slice(ready, func(i, j int) bool {
					return index[ready[i]] < index[ready[j]]
				})
			}
		}
	}
	if len(out) != len(plugins) {
		panic(fmt.Sprintf("special: plugin ordering cycle among %v", pluginNames(plugins)))
	}
	return out
}

func pluginNames(plugins []Plugin) []string {
	names := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		names = append(names, plugin.Name())
	}
	return names
}

// Plugins 已注册的插件（副本，调用方改不坏注册表）。
func Plugins() []Plugin {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]Plugin, len(registry.plugins))
	copy(out, registry.plugins)
	return out
}

// Route 按注册顺序询问路由插件；第一个明确认领请求的决定生效。
func Route(body []byte, request *Request, state *domain.State) (RouteDecision, bool) {
	if state == nil || !state.SpecialEnabled() {
		return RouteDecision{}, false
	}
	for _, p := range Plugins() {
		if state.SpecialPluginOff(p.Name()) {
			continue
		}
		router, ok := p.(RoutePlugin)
		if !ok {
			continue
		}
		if decision, matched := router.Route(body, request, state); matched {
			decision.Plugin = p.Name()
			return decision, true
		}
	}
	return RouteDecision{}, false
}

// Statuses 汇总所有启用插件贡献的状态行。
func Statuses(state *domain.State) []StatusItem {
	if state == nil || !state.SpecialEnabled() {
		return nil
	}
	var out []StatusItem
	for _, p := range Plugins() {
		if state.SpecialPluginOff(p.Name()) {
			continue
		}
		if reporter, ok := p.(StatusProvider); ok {
			out = append(out, reporter.Status(state)...)
		}
	}
	return out
}

func Bindings(state *domain.State) []domain.Binding {
	if state == nil || !state.SpecialEnabled() {
		return nil
	}
	var out []domain.Binding
	for _, p := range Plugins() {
		if state.SpecialPluginOff(p.Name()) {
			continue
		}
		if provider, ok := p.(BindingProvider); ok {
			out = append(out, provider.Bindings(state)...)
		}
	}
	return out
}

// MetricHint 让指标说明跟着产生指标的插件走，避免 CLI 维护插件名 switch。
func MetricHint(key string) (string, bool) {
	for _, p := range Plugins() {
		provider, ok := p.(MetricProvider)
		if !ok {
			continue
		}
		for _, metric := range provider.Metrics() {
			if key == "special."+p.Name()+"."+metric.Action {
				return metric.Hint, true
			}
		}
	}
	return "", false
}

func AuditResponse(body []byte) string {
	var reports []string
	for _, plugin := range Plugins() {
		if auditor, ok := plugin.(ResponseAuditor); ok {
			if report := auditor.AuditResponse(body); report != "" {
				reports = append(reports, plugin.Name()+":\n"+report)
			}
		}
	}
	return strings.Join(reports, "\n")
}

// ToolLoopNeedsRebase 询问匹配 candidate 的插件是否需要先有损重建，才能接手
// origin 产生的未闭合 tool loop。
func ToolLoopNeedsRebase(originProvider, originModel string, candidate *Request,
	off func(name string) bool) (bool, string) {
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		migrator, ok := p.(ToolLoopMigrator)
		if !ok || !p.Match(candidate) {
			continue
		}
		if needed, why := migrator.NeedsToolLoopRebase(
			originProvider, originModel, candidate); needed {
			return true, p.Name() + ": " + why
		}
	}
	return false, ""
}

// RebaseToolLoop 让声明需要 rebase 的插件改写请求。失败开放：插件出错时返回
// 原 body 和一条可见 note，由上游给出真实错误，不让补丁本身切断整条链。
func RebaseToolLoop(body []byte, originProvider, originModel string, candidate *Request,
	off func(name string) bool) Result {
	res := Result{Body: body}
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		migrator, ok := p.(ToolLoopMigrator)
		if !ok || !p.Match(candidate) {
			continue
		}
		needed, _ := migrator.NeedsToolLoopRebase(originProvider, originModel, candidate)
		if !needed {
			continue
		}
		out, note, err := migrator.RebaseToolLoop(body, candidate)
		if err != nil {
			note := "有损 tool loop 重建跳过（" + err.Error() + "）"
			res.Notes = []string{p.Name() + ": " + note}
			return res
		}
		if note == "" || out == nil {
			return res
		}
		res.Body, res.Changed = out, true
		res.Notes = []string{p.Name() + ": " + note}
		res.Events = []Event{{Plugin: p.Name(), Action: "tool_loop_rebase", Note: note}}
		return res
	}
	return res
}

// Result 一次 special_treatment 的结果。
type Result struct {
	Body    []byte   // Changed 为 false 时等于传进来的 body
	Notes   []string // 形如 "deepseek: 注入 thinking…"，逐条写日志
	Events  []Event
	Changed bool
}

type Event struct {
	Plugin string
	Action string
	Note   string
}

func (e Event) MetricKey() string {
	if e.Plugin == "" {
		return ""
	}
	if e.Action == "" || e.Action == "rewrite" {
		return "special." + e.Plugin
	}
	return "special." + e.Plugin + "." + e.Action
}

// Apply 按注册顺序跑一遍所有匹配的插件，串联改写。
//
// 装饰器链的完整语义：body 一路传（res.Body = out），上下文跟着 body 走
// （每步后 syncContextModel）——下一个插件拿到的 body 和 r.Model 都是上
// 一个插件改过的最新版，不存在「body 已被改、上下文还是旧的」的窗口。
//
// off 用来跳过被用户单独关掉的插件（可以传 nil）。
// 任何一个插件报错都只影响它自己：记一条 note，body 保持上一个插件的结果。
func Apply(body []byte, r *Request, off func(name string) bool) Result {
	res := Result{Body: body}
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		if !p.Match(r) {
			continue
		}
		out, notes, err := p.Apply(res.Body, r)
		if err != nil {
			note := "跳过（" + err.Error() + "），按原样发"
			res.Notes = append(res.Notes, p.Name()+": "+note)
			continue
		}
		if len(notes) == 0 || out == nil {
			continue // 这个插件这次没什么要补的
		}
		res.Body = out
		res.Changed = true
		for _, n := range notes {
			res.Notes = append(res.Notes, p.Name()+": "+n)
			res.Events = append(res.Events, Event{Plugin: p.Name(), Action: "rewrite", Note: n})
		}
		syncContextModel(res.Body, r)
	}
	return res
}

// syncContextModel 让上下文跟上 body：从 body 读回顶层 model 写进 r.Model。
// 这是链上唯一需要维护的派生字段（其余字段不来自 body）。读不出就保持
// 原值——绝不让一个改坏了的 body 把上下文也带坏。
func syncContextModel(body []byte, r *Request) {
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" && m != r.Model {
		r.Model = m
	}
}
