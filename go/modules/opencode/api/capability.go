package api

import modules "github.com/rzbdz/newgate/go/component"

type Client struct {
	AgentID string
}

var Capability = modules.One[Client]("client-family.opencode")
