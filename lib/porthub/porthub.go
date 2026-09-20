// Package porthub 是「一个端口上挂多个服务」的机制：一张 **前缀 → http.Handler**
// 的表，谁想在这个端口上再开一个 service，就往表里挂一条。
//
// 为什么做成机制：一个 gateway 只监听一个端口，这是部署上的硬约束（防火墙、反代、
// Tailscale ACL 都按端口配）。web 界面、将来别的服务都想住在这个端口上，而「往同一
// 张表里挂东西」这件事跟卖什么产品无关。
//
// 它只做三件事：记住谁挂了什么、按**最长前缀**查、拒绝抢保留前缀。**它不监听**——
// 真正服务的是 gateway 那个 http.Server（分派见 modules/gateway/forward），所以这里
// 的挂载是进程内的注册：不产生 socket、不产生 goroutine，因此在每条 `newgate …`
// 命令里被调用也是安全的（模模块的 Start 每条命令都会跑，这条约束见
// modules/i18n 的注释）。
//
// 与 pluginmanager 的开关点不同：那是「要不要这个功能」，这里是「这个 service 挂在
// 哪个路径上」。关掉 porthub 模块的后果是**消费者拿不到这张表**（capability 不在），
// 于是它们按 fallback 自己监听一个端口——语义干净，不需要在分派处写特判。
package porthub

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Mount 是一条已挂载的记录，给诊断与 status 用。
type Mount struct {
	Prefix string
	Owner  string // 谁挂的（模块名）；冲突时报错要点名
}

// Registry 是挂载表。零值不可用，用 NewRegistry 或 Default。
type Registry struct {
	mu     sync.RWMutex
	mounts map[string]mount
}

type mount struct {
	handler http.Handler
	owner   string
}

// reserved 是不能被抢的前缀。
//
// 理由不是「不好看」：`/a` 与 `/v1` 是数据面（转发路径），`/__newgate` 是控制面
// 命名空间。被抢的症状是**本该发给本机控制面的请求被当数据面转发给上游**——
// forward.go 里那句「绝不盲发 /__newgate/upgrade（那会被 catch-all 转发到上游）」
// 记的就是这个坑。所以这里当场报错，而不是让后来者悄悄覆盖。
var reserved = []string{"/v1", "/a", "/__newgate"}

// NewRegistry 造一张空表。
func NewRegistry() *Registry {
	return &Registry{mounts: map[string]mount{}}
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default 是进程级的那张表。
//
// 为什么这里允许一个全局：它和 metrics.Default 是同一类东西——除去「谁挂了什么」
// 没有别的状态，而这件事在一个进程里天然只有一份（那个 http.Server 也只有一份）。
// 有生命周期的东西仍然走 capability（modules/porthub），全局只解决「分派处怎么
// 拿到表」这一个问题。
func Default() *Registry {
	defaultOnce.Do(func() { defaultReg = NewRegistry() })
	return defaultReg
}

// Mount 把 h 挂到 prefix 下。owner 是挂载者的名字，只用于报错与诊断。
//
// 返回的 Release 撤销这次挂载，幂等；Stop 里必须调用（没有 Release 就没有生命
// 周期，见 modules/gateway/module.go 里那段反例分析）。
func (r *Registry) Mount(prefix, owner string, h http.Handler) (func(), error) {
	p, err := normalize(prefix)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, i18n.E("porthub: the handler {owner} wants to mount at {prefix} is nil",
			i18n.A{"owner": owner, "prefix": p})
	}
	for _, res := range reserved {
		if p == res || strings.HasPrefix(p, res+"/") || strings.HasPrefix(res, p+"/") {
			return nil, i18n.E("porthub: prefix {prefix} (mounted by {owner}) collides with the "+
				"reserved prefix {reserved}, which belongs to the data plane / control plane "+
				"namespace — claiming it would send requests meant for this machine upstream "+
				"as if they were data-plane traffic", i18n.A{"prefix": p, "owner": owner, "reserved": res})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, dup := r.mounts[p]; dup {
		return nil, i18n.E("porthub: prefix {prefix} is already mounted by {owner} — two services "+
			"on one path means only one of them ever sees a request, so this errors instead of "+
			"letting the later one win silently", i18n.A{"prefix": p, "owner": prev.owner})
	}
	r.mounts[p] = mount{handler: h, owner: owner}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if m, ok := r.mounts[p]; ok && m.owner == owner {
			delete(r.mounts, p)
		}
	}, nil
}

// Lookup 按最长前缀查。第二个返回值为 false 表示这张表不管这个路径，
// 调用方该走自己的路（gateway 那边就是落回转发）。
func (r *Registry) Lookup(path string) (http.Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := ""
	var found mount
	for p, m := range r.mounts {
		if !matches(p, path) {
			continue
		}
		if len(p) > len(best) {
			best, found = p, m
		}
	}
	if best == "" {
		return nil, false
	}
	return found.handler, true
}

// Mounts 返回快照，按前缀排序（诊断、`newgate status` 用）。
func (r *Registry) Mounts() []Mount {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Mount, 0, len(r.mounts))
	for p, m := range r.mounts {
		out = append(out, Mount{Prefix: p, Owner: m.owner})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// matches 是前缀匹配的判据：`/ui` 命中 `/ui` 与 `/ui/...`，但**不**命中 `/uix`
// ——按字节前缀匹配会在这里悄悄多认一个 service。
func matches(prefix, path string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// normalize 收敛写法：必须以 / 开头、不能是 /（那是 catch-all）、结尾的 / 去掉。
func normalize(prefix string) (string, error) {
	if prefix == "" || prefix[0] != '/' {
		return "", i18n.E("porthub: a prefix must start with /, got {prefix}",
			i18n.A{"prefix": prefix})
	}
	p := strings.TrimSuffix(prefix, "/")
	if p == "" {
		return "", i18n.E("porthub: the prefix cannot be / — that is the whole port. Give a path "+
			"prefix instead, e.g. /ui", nil)
	}
	return p, nil
}
