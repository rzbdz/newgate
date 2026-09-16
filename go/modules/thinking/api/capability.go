package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	gatewayapi "github.com/rzbdz/newgate/go/modules/gateway/api"
)

type Service interface {
	BestEffortDisable(body []byte, request *gatewayapi.Request) ([]byte, []string, error)
}

var Capability = modules.One[Service]("thinking")
