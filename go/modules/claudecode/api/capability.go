package api

import modules "github.com/rzbdz/newgate/go/component"

type Client struct {
	Name    string
	AgentID string
}

var Capability = modules.One[Client]("client-family.claudecode")
