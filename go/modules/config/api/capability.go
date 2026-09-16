package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
)

type Config struct{}
type ExtraRole = domain.ExtraRole
type Step = resolve.Step
type Skip = resolve.Skip

var Capability = modules.One[Config]("config")
