package api

import modules "github.com/rzbdz/newgate/go/component"

type Model struct {
	MatchTarget func(model, provider, baseURL string) bool
}

var Capability = modules.One[Model]("model-family.deepseek")
