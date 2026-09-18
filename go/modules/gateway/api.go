package gateway

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/gateway/quirk"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// Request 是 special treatment 判断请求上下文所需的稳定事实集合。
// 它不暴露转发器内部对象，插件因而不能控制重试、连接或响应生命周期。
// 契约本体在 special（插件层）定义，这里只做名字转发。
type Request = special.Request

// Plugin 是网关请求改写扩展点。Match 负责缩小适用范围，
// Apply 只做字节级手术并返回可记录的 notes；错误由调用方按 fail-open 处理。
type Plugin = special.Plugin

// Gateway 是网关模块公开的控制面，只允许注册请求插件，
// 不把数据面 handler 或内部 registry 泄漏给其他模块。
type Gateway interface {
	RegisterRequestHook(Plugin) (modules.Release, error)

	// Quirks 是那张「上游毛病」表（见 modules/gateway/quirk）。
	//
	// 它暴露出来是因为**判据得由拥有补丁的模块注册**：`该模型始终思考` 这条
	// 签名的原文是 GLM / DeepSeek / Kimi 各自的方言，补丁（disabled → enabled +
	// reasoning_effort）住在 modules/thinking，所以签名也该由它注册进来——与
	// `breaker.RegisterShapeDetector`、`special` 的 st-<上游>.go 是同一条规矩：
	// **core 里不出现上游专有字符串**。
	//
	// 数据面在每次请求上把同一张表交给插件（special.Request.Quirks），所以注册
	// 进来的判据对热路径立刻生效。
	Quirks() *quirk.Table
}

// Capability 标识进程中唯一的网关控制面。
var Capability = modules.NewCapability[Gateway]("gateway")
