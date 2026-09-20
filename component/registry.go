package component

import "sync"

// Registry 是「注册 → 撤销」这类贡献的通用账本：单调 token + 按 token 精确
// 撤销 + 可选的准入检查。
//
// 为什么要有它
//
// 跨模块注入在 2026-09-17 收敛成一条路：**要往别人的扩展点插东西，就消费它
// 的 service 并调 RegisterX**，不再用 `Provides: Provide(别人的能力, 值)`
// （那条路没有 Release、没有查重、没有生命周期——两个模块认领同一个命令名会
// 静默先到先得）。收敛之后，「注册一张带撤销的表」这件事在每个 owner 里都要
// 写一遍，所以把它抽到这里。
//
// 与产品层里那几份手写账本的关系：它们**暂时保留原样**。那几份各有真实的差异
// （插件拓扑排序与 copy-on-write、带 Refresh 的读侧、多个异质注册表共享一张
// token 表），强行归并会造出更差的抽象。这里先在一处验证，验证过再逐个迁。
//
// 相比那三份实现，这里少一张 `tokens map[string]uint64`：那张表是为了容忍
// 「同名重复注册」——新值覆盖旧值，旧值的 Release 变成 no-op。而我们**禁止**
// 重复注册（交给调用方的 check），重复的当场报错，于是只需要单调计数器。
//
// 零值可用：新建的 module service 直接内嵌一个 Registry[T] 字段即可，不必构造。
type Registry[T any] struct {
	mu    sync.Mutex
	items []registryEntry[T]
	next  uint64
}

type registryEntry[T any] struct {
	token uint64
	value T
}

// Register 先把 value 交给 check 审一遍，通过了才加入账本，返回只属于本次
// 注册的撤销句柄。
//
// check 在**写锁内**被调用，所以「查重」和「插入」对并发注册是原子的：
// 两个 goroutine 同时注册同一个名字，第二个一定看得见第一个。check 拿到的是
// 当前全部贡献的**快照**（改它不影响账本），返回非 nil 表示拒绝，此时账本
// 不变、Release 为 nil。
//
// check 传 nil 表示不检查（诊断这类可叠加、没有键命名空间的贡献就是这种）。
func (r *Registry[T]) Register(value T, check func([]T) error) (Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if check != nil {
		if err := check(r.snapshotLocked()); err != nil {
			return nil, err
		}
	}

	r.next++
	token := r.next
	r.items = append(r.items, registryEntry[T]{token: token, value: value})

	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, item := range r.items {
			if item.token != token {
				continue
			}
			r.items = append(r.items[:i], r.items[i+1:]...)
			return nil
		}
		// 陈旧 Release：这一条已经被撤过了（或者根本没插进来）。返回 nil 而
		// 不是报错，是为了让 Stop 幂等——组件的 Stop 可能被调用不止一次，
		// 释放路径上多一次「已经没了」不该被当成故障。
		return nil
	}, nil
}

// All 返回全部贡献的快照，顺序与注册顺序一致。
//
// 返回的是副本：调用方拿着它遍历时可能有别的模块在注册，不能把内部切片交出去。
// 空账本返回 nil（不是空切片），调用方按 len()==0 判断即可。
func (r *Registry[T]) All() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *Registry[T]) snapshotLocked() []T {
	if len(r.items) == 0 {
		return nil
	}
	out := make([]T, 0, len(r.items))
	for _, item := range r.items {
		out = append(out, item.value)
	}
	return out
}
