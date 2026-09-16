package api

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
)

type ExtraRole = domain.ExtraRole
type Step = resolve.Step
type Skip = resolve.Skip

type RoleProvider interface {
	Source() string
	Roles() ([]ExtraRole, error)
}

type RoleWatchProvider interface {
	WatchFiles() []string
}

type Config interface {
	RegisterRoleProvider(RoleProvider) (modules.Release, error)
}

var Capability = modules.One[Config]("config")
