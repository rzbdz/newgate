// Package confighook owns agent/configuration extension registries. It is a
// gateway consumer because every installed agent configuration targets the
// local gateway endpoint.
package confighook

import (
	"fmt"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
)

type registry struct {
	mu     sync.RWMutex
	agents map[string]*agentapi.Agent
	fields map[string]string
	tokens map[string]uint64
	next   uint64
}

var (
	_ agentapi.ConfigHooks  = (*registry)(nil)
	_ agentapi.AgentCatalog = (*registry)(nil)
)

func New() modules.Component {
	registry := &registry{
		agents: make(map[string]*agentapi.Agent),
		fields: make(map[string]string),
		tokens: make(map[string]uint64),
	}
	return modules.Component{
		Name: "config-hook",
		Provides: []modules.Provision{
			modules.Provide(agentapi.ConfigHooksCapability, agentapi.ConfigHooks(registry)),
			modules.Provide(agentapi.AgentCatalogCapability, agentapi.AgentCatalog(registry)),
		},
	}
}

func (r *registry) RegisterAgent(agent *agentapi.Agent) (modules.Release, error) {
	if agent == nil || agent.ID == "" {
		return nil, fmt.Errorf("agent ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[agent.ID]; exists {
		return nil, fmt.Errorf("duplicate agent %s", agent.ID)
	}
	token := r.newToken("agent:" + agent.ID)
	r.agents[agent.ID] = agent
	return r.release("agent:"+agent.ID, token, func() {
		delete(r.agents, agent.ID)
	}), nil
}

func (r *registry) BindTakeover(agentID string, takeover agentapi.ConfigTakeover) (modules.Release, error) {
	if takeover == nil {
		return nil, fmt.Errorf("nil config takeover for agent %s", agentID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("config takeover targets unknown agent %s", agentID)
	}
	if agent.Config != nil {
		return nil, fmt.Errorf("agent %s has multiple config takeovers", agentID)
	}
	token := r.newToken("takeover:" + agentID)
	agent.Config = takeover
	return r.release("takeover:"+agentID, token, func() {
		agent.Config = nil
	}), nil
}

func (r *registry) RegisterStateField(owner, name string) (modules.Release, error) {
	if name == "" {
		return nil, fmt.Errorf("state field name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner, exists := r.fields[name]; exists {
		return nil, fmt.Errorf("state field %s already registered by %s", name, owner)
	}
	token := r.newToken("field:" + name)
	r.fields[name] = owner
	return r.release("field:"+name, token, func() {
		delete(r.fields, name)
	}), nil
}

func (r *registry) newToken(key string) uint64 {
	r.next++
	r.tokens[key] = r.next
	return r.next
}

func (r *registry) release(key string, token uint64, remove func()) modules.Release {
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[key] != token {
			return nil
		}
		delete(r.tokens, key)
		remove()
		return nil
	}
}

func (r *registry) Get(id string) (*agentapi.Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	if !ok {
		return nil, false
	}
	clone := *agent
	clone.Bin = append([]string(nil), agent.Bin...)
	clone.Slots = append([]agentapi.Slot(nil), agent.Slots...)
	clone.UnsetEnv = append([]string(nil), agent.UnsetEnv...)
	return &clone, true
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
