package api

import modules "github.com/rzbdz/newgate/go/component"

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

type Plugin interface {
	Name() string
	Why() string
	Match(*Request) bool
	Apply(body []byte, request *Request) (out []byte, notes []string, err error)
}

type Gateway interface {
	RegisterRequestHook(Plugin)
	AgentBaseURL(port int, agentID string) string
}

var Capability = modules.One[Gateway]("gateway")
