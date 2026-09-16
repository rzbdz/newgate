package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ProbeGrade 把探活结果归一为少量稳定档位，供路由排序和 UI 共同解释。
type ProbeGrade string

const (
	ProbeFluent      ProbeGrade = "fluent"
	ProbeUsable      ProbeGrade = "usable"
	ProbeLaggy       ProbeGrade = "laggy"
	ProbeUnavailable ProbeGrade = "unavailable"
)

type probeResult struct {
	Grade     ProbeGrade
	LatencyMs int64
	CheckedAt time.Time
}

type latencyScore struct {
	EWMA    [4]float64
	Samples [4]int
}

// Breaker 是 daemon 内的全局 binding 健康表。熔断后不会按时间自动复活：
// 只有一次成功 probe 能把 binding 放回链，避免坏上游每分钟回来撞一次用户请求。
type Breaker struct {
	mu           sync.Mutex
	fails        map[string]int
	openedAt     map[string]time.Time
	reasons      map[string]string
	probes       map[string]probeResult
	scores       map[string]latencyScore
	file         string
	persistedAt  time.Time
	persistTimer *time.Timer
	onError      func(error)

	Threshold int           // 真实流量连续失败多少次开闸
	Cooldown  time.Duration // 最短隔离时间；到期仍需成功 probe 才能回链
}

func newBreaker() *Breaker {
	return &Breaker{
		fails:     map[string]int{},
		openedAt:  map[string]time.Time{},
		reasons:   map[string]string{},
		probes:    map[string]probeResult{},
		scores:    map[string]latencyScore{},
		Threshold: 2,
		Cooldown:  60 * time.Second,
	}
}

// Default 是 daemon 热路径与 probe 共享的进程级健康事实源。
var Default = newBreaker()

// SetErrorHandler 注入持久化错误出口；健康状态不能因后台写盘失败而静默丢失。
func (b *Breaker) SetErrorHandler(fn func(error)) {
	b.mu.Lock()
	b.onError = fn
	b.mu.Unlock()
}

func bindingKey(provider, model string) string { return provider + "\x00" + model }

// Available 只回答 binding 当前是否可进入候选链，不在读路径隐式解除熔断。
func (b *Breaker) Available(provider, model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	_, open := b.openedAt[key]
	return !open
}

// RecordSuccess 清零连续失败，但故意不解除熔断；恢复必须由独立 probe 证明。
func (b *Breaker) RecordSuccess(provider, model string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	b.fails[key] = 0
	// 成功的普通请求只刷新分数，不负责解封。熔断状态必须经过 probe。
}

// RecordFailure 返回 true 表示这次失败把闸打开了。
func (b *Breaker) RecordFailure(provider, model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	b.fails[key]++
	if b.fails[key] >= b.Threshold {
		b.openedAt[key] = time.Now()
		b.reasons[key] = "真实流量连续失败"
		b.persistLocked()
		return true
	}
	return false
}

// Open 立即熔断一个 binding。probe 已经是主动、独立的健康请求；再要求它
// 累计两次只会让已知不可用的模型继续占住真实请求链。
func (b *Breaker) Open(provider, model, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	b.fails[key] = b.Threshold
	b.openedAt[key] = time.Now()
	b.reasons[key] = reason
	b.persistLocked()
}