package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
)

// Service 提供与模型无关的 thinking 降级操作。
// 具体模型插件依赖这个端口，而不是反向依赖 thinking 的内部实现。
type Service interface {
	BestEffortDisable(body []byte, request *gatewayapi.Request) ([]byte, []string, error)
}

// Capability 标识唯一的 thinking 策略服务。
var Capability = modules.One[Service]("thinking")
