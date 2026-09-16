// Package roleprov 汇总**模块贡献的动态角色键**。
//
// 为什么要有这一层：档位（heavy/normal/mid/light/vision）是框架的能力阶梯，
// 每个客户端共享；但客户端插件常常自带更细的槽位体系——opencode 的
// oh-my-openagent 有 sisyphus / librarian / … 十几个 intra-agent，将来别的
// 插件还会有别的。这些键**叫什么、有几个、缺省跟谁走**是那个模块的知识，
// 不该 hard-code 进 core（docs/04-configuration.md）。
//
// 分工：
//
//	模块实现 Provider，经 Config 组件的 capability 注册；
//	框架（store 每次装快照、CLI 启动）调 Refresh，把结果灌进 domain。
//
// core 只认「键 → 缺省绑定」这张表，解析时与内置别名一视同仁。
package roleprov

import (
	"fmt"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/go/component"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
	"github.com/rzbdz/newgate/go/modules/config/domain"
)

// Provider 是配置 API 中动态档位提供者的本地别名，避免 roleprov 另造一套契约。
type Provider = configapi.RoleProvider

// WatchProvider 是提供者可选实现的热更新文件声明。
type WatchProvider = configapi.RoleWatchProvider

// Registry 聚合动态档位提供者，并用 token 维护每次注册的精确所有权。
type Registry struct {
	mu        sync.RWMutex
	providers []Provider
	tokens    map[string]uint64
	next      uint64
}

var (
	defaultMu       sync.RWMutex
	defaultRegistry = &Registry{}
)

// NewRegistry 创建隔离注册表，供每个 Config 组件实例独立拥有。
func NewRegistry() *Registry { return &Registry{} }

// SetDefault 设置旧数据面使用的默认注册表；新装配应使用 InstallDefault。
func SetDefault(registry *Registry) {
	if registry == nil {
		panic("roleprov: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

// InstallDefault 暂时接管兼容全局，并返回不会覆盖新 owner 的恢复函数。
func InstallDefault(registry *Registry) func() {
	if registry == nil {
		panic("roleprov: nil default registry")
	}
	defaultMu.Lock()
	previous := defaultRegistry
	defaultRegistry = registry
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		if defaultRegistry == registry {
			defaultRegistry = previous
		}
		defaultMu.Unlock()
	}
}

func currentRegistry() *Registry {
	defaultMu.RLock()
	registry := defaultRegistry
	defaultMu.RUnlock()
	return registry
}

// Register 是旧数据面的兼容入口；生产装配通过 Config capability 注册。
func Register(p Provider) {
	if _, err := currentRegistry().Register(p); err != nil {
		panic(err)
	}
}

// Register 加入一个来源唯一的动态档位提供者，并返回所有权 Release。
func (r *Registry) Register(p Provider) (modules.Release, error) {
	if p == nil || p.Source() == "" {
		return nil, fmt.Errorf("roleprov: provider source is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.providers {
		if q.Source() == p.Source() {
			return nil, fmt.Errorf("roleprov: duplicate provider %s", p.Source())
		}
	}
	if r.tokens == nil {
		r.tokens = make(map[string]uint64)
	}
	r.next++
	token := r.next
	r.tokens[p.Source()] = token
	r.providers = append(r.providers, p)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[p.Source()] != token {
			return nil
		}
		delete(r.tokens, p.Source())
		for i, provider := range r.providers {
			if provider.Source() == p.Source() {
				r.providers = append(r.providers[:i], r.providers[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}