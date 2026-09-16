// Package builtin owns the framework's default loader and process-wide module
// set. Concrete module implementations remain sibling packages.
package builtin

import (
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/claudecode"
	"github.com/rzbdz/newgate/go/modules/claudecode_deepseek"
	"github.com/rzbdz/newgate/go/modules/claudecode_glm"
	"github.com/rzbdz/newgate/go/modules/cli"
	"github.com/rzbdz/newgate/go/modules/config"
	"github.com/rzbdz/newgate/go/modules/confighook"
	"github.com/rzbdz/newgate/go/modules/deepseek"
	gatewaycomponent "github.com/rzbdz/newgate/go/modules/gateway"
	"github.com/rzbdz/newgate/go/modules/glm"
	"github.com/rzbdz/newgate/go/modules/opencode"
	"github.com/rzbdz/newgate/go/modules/opencodeomo"
	runtimecomponent "github.com/rzbdz/newgate/go/modules/runtime"
	"github.com/rzbdz/newgate/go/modules/thinking"
)

type Loader struct{}

func (Loader) Load() ([]modules.Component, error) {
	providers := []modules.Provider{
		config.New(),
		gatewaycomponent.New(),
		confighook.New(),
		runtimecomponent.New(),
		thinking.New(),
		claudecode.New(),
		deepseek.New(),
		glm.New(),
		claudecode_deepseek.New(),
		claudecode_glm.New(),
		opencode.New(),
		opencodeomo.New(),
		cli.New(),
	}
	components := make([]modules.Component, 0, len(providers))
	for _, provider := range providers {
		components = append(components, provider.Component())
	}
	return components, nil
}
