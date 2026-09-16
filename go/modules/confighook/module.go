// Package confighook owns agent/configuration extension registries. It is a
// gateway consumer because every installed agent configuration targets the
// local gateway endpoint.
package confighook

import (
	"context"
	"fmt"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/confighook/roleprov"
	"github.com/rzbdz/newgate/go/modules/contracts"
)

type registry struct {
	mu      sync.RWMutex
	gateway contracts.Gateway
	roles   *roleprov.Registry
	agents  map[string]*agentapi.Agent
	fields  map[string]string
}

type provider struct{ registry *registry }

var (
	_ contracts.ConfigHooks  = (*registry)(nil)
	_ contracts.AgentCatalog = (*registry)(nil)
	_ modules.Provider       = (*provider)(nil)
)

func New() modules.Provider {
	return provider{registry: &registry{
		agents: make(map[string]*agentapi.Agent),
		fields: make(map[string]string),
		roles:  roleprov.NewRegistry(),
	}}
}

func (p provider) Component() modules.Component {
	var restoreRoles func()
	return modules.Component{
		Name:     "config-hook",
		Requires: []modules.Requirement{modules.Need(contracts.GatewayCapability)},
		Provides: []modules.Provision{
			modules.Provide(contracts.ConfigHooksCapability, contracts.ConfigHooks(p.registry)),
			modules.Provide(contracts.AgentCatalogCapability, contracts.AgentCatalog(p.registry)),
		},
		Start: func(ctx modules.Context) error {
			p.registry.gateway = modules.MustGet(ctx, contracts.GatewayCapability)
			restoreRoles = roleprov.InstallDefault(p.registry.roles)
			return nil
		},
		Stop: func(context.Context) error {
			if restoreRoles != nil {
				restoreRoles()
			}
			return nil
		},
	}
}

func (r *registry) RegisterAgent(agent *agentapi.Agent) error {
	if agent == nil || agent.ID == "" {
		return fmt.Errorf("agent ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[agent.ID]; exists {
		return fmt.Errorf("duplicate agent %s", agent.ID)
	}
	r.agents[agent.ID] = agent
	return nil
}

func (r *registry) BindTakeover(agentID string, takeover agentapi.ConfigTakeover) error {
	if takeover == nil {
		return fmt.Errorf("nil config takeover for agent %s", agentID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.agents[agentID]
	if !ok {
		return fmt.Errorf("config takeover targets unknown agent %s", agentID)
	}
	if agent.Config != nil {
		return fmt.Errorf("agent %s has multiple config takeovers", agentID)
	}
	agent.Config = takeover
	return nil
}

func (r *registry) RegisterRoleProvider(provider roleprov.Provider) {
	r.roles.Register(provider)
}

func (r *registry) RegisterStateField(owner, name string) error {
	if name == "" {
		return fmt.Errorf("state field name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner, exists := r.fields[name]; exists {
		return fmt.Errorf("state field %s already registered by %s", name, owner)
	}
	r.fields[name] = owner
	return nil
}

func (r *registry) Get(id string) (*agentapi.Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	return agent, ok
}

func (r *registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *registry) StateFieldOwner(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	owner, ok := r.fields[name]
	return owner, ok
}
