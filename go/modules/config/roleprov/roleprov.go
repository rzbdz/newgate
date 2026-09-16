// Package roleprov 汇总**模块贡献的动态角色键**。
//
// 为什么要有这一层：档位（heavy/normal/mid/light/vision）是框架的能力阶梯，
// 每个客户端共享；但客户端插件常常自带更细的槽位体系——opencode 的
// oh-my-openagent 有 sisyphus / librarian / … 十几个 intra-agent，将来别的
// 插件还会有别的。这些键**叫什么、有几个、缺省跟谁走**是那个模块的知识，
// 不该 hard-code 进 core（docs/18 §1.3）。
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

type Provider = configapi.RoleProvider
type WatchProvider = configapi.RoleWatchProvider

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

func NewRegistry() *Registry { return &Registry{} }

func SetDefault(registry *Registry) {
	if registry == nil {
		panic("roleprov: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

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

// Refresh 重新问一遍每个已登记的 provider，把结果灌进 domain。
//
// 调用点：store.Load（每次装配置快照）——角色键是配置的一部分，改了
// omo-slots.json 应当和改 providers.json 一样自动生效，不需要重启。
//
// 单个 provider 读文件失败不影响别的：记一条错误继续（失败开放），
// 那个模块的键退化成「没注册」，用户看到的是一句可查的报错而不是全线停摆。
func Refresh() []error {
	registry := currentRegistry()
	registry.mu.RLock()
	ps := append([]Provider(nil), registry.providers...)
	registry.mu.RUnlock()

	var (
		out  []domain.ExtraRole
		errs []error
		seen = map[string]string{} // key -> 谁登记的
	)
	for _, p := range ps {
		roles, err := p.Roles()
		if err != nil {
			errs = append(errs, fmt.Errorf("模块 %s 的角色键读取失败: %w", p.Source(), err))
			continue
		}
		for _, r := range roles {
			if r.Key == "" {
				continue
			}
			// 阶梯档位是框架的，模块不许覆盖——那会让「heavy 是什么意思」
			// 取决于装了哪个插件，排查时无从下手。
			if domain.IsRole(r.Key) {
				errs = append(errs, fmt.Errorf("模块 %s 想注册 %q，但这是内置档位名，已忽略", p.Source(), r.Key))
				continue
			}
			if who, dup := seen[r.Key]; dup {
				errs = append(errs, fmt.Errorf("键 %q 被 %s 和 %s 同时注册，保留先登记的", r.Key, who, p.Source()))
				continue
			}
			seen[r.Key] = p.Source()
			if r.Source == "" {
				r.Source = p.Source()
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	domain.SetExtraRoles(out)
	return errs
}

// Sources 已登记的模块短名（doctor / `newgate omo` 用）。
func Sources() []string {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]string, 0, len(registry.providers))
	for _, p := range registry.providers {
		out = append(out, p.Source())
	}
	sort.Strings(out)
	return out
}

func WatchFiles() []string {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	var files []string
	for _, provider := range registry.providers {
		if watcher, ok := provider.(WatchProvider); ok {
			files = append(files, watcher.WatchFiles()...)
		}
	}
	sort.Strings(files)
	return files
}
