// Package confighook 管理“newgate 能接管哪些客户端”这份运行时目录。
//
// 客户端模块注册 Agent 描述符，配置插件再为该 Agent 绑定可逆的 takeover。
// runtime 只需要查询并启动客户端，不能修改目录；因此同一注册表拆成
// ConfigHooks 写端口和 AgentCatalog 读端口。这个拆分不是形式上的接口隔离，
// 而是防止启动路径获得注册权限。
//
// 每次写入都返回带 token 的 Release。旧组件停止时只能撤销自己那次注册，
// 不会误删热重载或新应用实例后来写入的值。
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

// New 创建同时实现写入端口和只读目录端口的注册表。
// 两个 capability 共享同一事实源，但通过不同接口限制消费者权限。
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

// RegisterAgent 原子加入客户端描述符，并用 token 防止过期 Release 删除后来的注册。
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

// BindTakeover 把配置接管绑定到已存在客户端，避免产生无法启动的孤立配置插件。
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

// RegisterStateField 为共享状态文件登记字段所有者，提前拒绝跨模块字段冲突。
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

// Get 返回描述符副本，防止只读消费者修改注册表持有的切片。
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

// Names 返回稳定排序的客户端名，使 CLI 输出和测试结果可复现。
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
