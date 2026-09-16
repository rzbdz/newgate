package api

import modules "github.com/rzbdz/newgate/go/component"

type Runtime struct{}

var Capability = modules.One[Runtime]("runtime")
