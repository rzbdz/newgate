package api

import modules "github.com/rzbdz/newgate/go/component"

// Request 是 special treatment 判断请求上下文所需的稳定事实集合。
// 它不暴露转发器内部对象，插件因而不能控制重试、连接或响应生命周期。
type Request struct {
	InModel  string
	Tier     string
	Model    string
	Provider string
	BaseURL  string
	Protocol string
	Path     string
	Stream   bool
	Agent    string
}

// Plugin 是网关请求改写扩展点。Match 负责缩小适用范围，
// Apply 只做字节级手术并返回可记录的 notes；错误由调用方按 fail-open 处理。
type Plugin interface {
	Name() string
	Why() string
	Match(*Request) bool
	Apply(body []byte, request *Request) (out []byte, notes []string, err error)
}

// Gateway 是网关模块公开的控制面，只允许注册请求插件，
// 不把数据面 handler 或内部 registry 泄漏给其他模块。
type Gateway interface {
	RegisterRequestHook(Plugin) (modules.Release, error)
}

// Capability 标识进程中唯一的网关控制面。
var Capability = modules.One[Gateway]("gateway")
