package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
)

type Runtime interface {
	Launch(agent *agentapi.Agent, args []string, profile string) int
}

var Capability = modules.One[Runtime]("runtime")
