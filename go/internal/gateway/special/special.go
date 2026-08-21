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

// Request 是插件能看到的这次转发的上下文，只读。
//
// 故意不含 http.Request：插件只该看请求的**语义归属**（发给谁、什么模型），
// 看不到也改不了头、认证、连接。想动这些的补丁不属于这一层。
type Request struct {
	InModel  string // 客户端原本写的 model（语义档位名，如 "heavy"）
	Tier     string // 归一化后的档位
	Model    string // 即将发给上游的真实模型名
	Provider string // provider 名（providers.json 里的 key）
	BaseURL  string // 上游 base URL
	Protocol string // "anthropic" / "openai"
	Path     string // 请求路径后缀，如 /messages
	Stream   bool
}

// Plugin 一个特殊照顾模块。实现放在 st-<名字>.go，在 init() 里 Register。
type Plugin interface {
	// Name 稳定的短名，用户用它开关这个插件（newgate st off <name>）。
	Name() string
	// Why 一句话说明它为什么存在：哪个上游、什么报错。
	// 这句话会直接展示给用户，也是将来判断「还需不需要它」的唯一依据。
	Why() string
	// Match 这次请求是不是该由我照顾。
	Match(r *Request) bool
	// Apply 返回改写后的 body 和「我改了什么」。
	//
	// 契约：
	//   - 什么都没改 → notes 为空（out 会被忽略），调用方保持原字节；
	//   - 改了      → notes 非空且 out 有效；
	//   - 出错      → err 非 nil，调用方丢弃 out，按原样发。
	Apply(body []byte, r *Request) (out []byte, notes []string, err error)
}

var registry []Plugin

// Register 注册一个插件。只在 init() 里调用，所以不用加锁。
// 顺序 = 注册顺序 = 同包内文件名顺序，是确定的。
func Register(p Plugin) { registry = append(registry, p) }

// Plugins 已注册的插件（副本，调用方改不坏注册表）。
func Plugins() []Plugin {
	out := make([]Plugin, len(registry))
	copy(out, registry)
	return out
}

// Result 一次 special_treatment 的结果。
type Result struct {
	Body    []byte   // Changed 为 false 时等于传进来的 body
	Notes   []string // 形如 "deepseek: 注入 thinking…"，逐条写日志
	Changed bool
}

// Apply 按注册顺序跑一遍所有匹配的插件，串联改写。
//
// off 用来跳过被用户单独关掉的插件（可以传 nil）。
// 任何一个插件报错都只影响它自己：记一条 note，body 保持上一个插件的结果。
func Apply(body []byte, r *Request, off func(name string) bool) Result {
	res := Result{Body: body}
	for _, p := range registry {
		if off != nil && off(p.Name()) {
			continue
		}
		if !p.Match(r) {
			continue
		}
		out, notes, err := p.Apply(res.Body, r)
		if err != nil {
			res.Notes = append(res.Notes, p.Name()+": 跳过（"+err.Error()+"），按原样发")
			continue
		}
		if len(notes) == 0 || out == nil {
			continue // 这个插件这次没什么要补的
		}
		res.Body = out
		res.Changed = true
		for _, n := range notes {
			res.Notes = append(res.Notes, p.Name()+": "+n)
		}
	}
	return res
}
