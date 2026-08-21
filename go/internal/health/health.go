package health

import (
	"sync"
	"time"
)

// Breaker 极简熔断：连续失败 N 次就把这个 provider 摘掉一段时间。
// 目的是别在上游已经挂了的时候还每个请求都去撞一次。
type Breaker struct {
	mu        sync.Mutex
	fails     map[string]int
	openUntil map[string]time.Time

	Threshold int           // 连续失败多少次开闸
	Cooldown  time.Duration // 开闸后多久再试
}

var Default = &Breaker{
	fails:     map[string]int{},
	openUntil: map[string]time.Time{},
	Threshold: 2,
	Cooldown:  60 * time.Second,
}

func (b *Breaker) Available(provider string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.openUntil[provider]
	if !ok {
		return true
	}
	if time.Now().After(until) {
		delete(b.openUntil, provider)
		b.fails[provider] = 0
		return true
	}
	return false
}

func (b *Breaker) RecordSuccess(provider string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails[provider] = 0
	delete(b.openUntil, provider)
}

// RecordFailure 返回 true 表示这次失败把闸打开了。
func (b *Breaker) RecordFailure(provider string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails[provider]++
	if b.fails[provider] >= b.Threshold {
		b.openUntil[provider] = time.Now().Add(b.Cooldown)
		return true
	}
	return false
}

type Status struct {
	Provider string
	Fails    int
	OpenFor  time.Duration
}

func (b *Breaker) Snapshot() []Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Status
	seen := map[string]bool{}
	for p, n := range b.fails {
		if n == 0 {
			continue
		}
		seen[p] = true
		s := Status{Provider: p, Fails: n}
		if u, ok := b.openUntil[p]; ok {
			s.OpenFor = time.Until(u)
		}
		out = append(out, s)
	}
	for p, u := range b.openUntil {
		if !seen[p] {
			out = append(out, Status{Provider: p, Fails: b.fails[p], OpenFor: time.Until(u)})
		}
	}
	return out
}

// Retryable 判断这个失败该不该转移到备用。
// 只转移「确定没产生副作用」的失败——已经开始吐流的绝不转移。
func Retryable(statusCode int, connErr bool) bool {
	return ShouldAdvance(statusCode, false) || connErr
}

// ShouldAdvance 决定这个上游状态码该不该沿链往下走。见 docs/18 §6。
//
//	连接失败/超时/429/5xx  → 走：明确的可用性问题
//	404 模型不存在         → 走：这家没这个模型
//	401/403                → 走，但调用方要大声告警：凭证坏了不该让请求死，
//	                          但必须让用户知道是 key 问题不是模型问题
//	400                    → 默认不走（fallbackOn400 可开）。schema 类问题
//	                          应在 schemarepair 根治，而不是靠换 provider 掩盖；
//	                          若是客户端自己的 bug，往下走就是拿坏请求撞遍所有上游
//	其它 4xx               → 不走：请求本身有问题，换谁都一样
func ShouldAdvance(statusCode int, fallbackOn400 bool) bool {
	switch statusCode {
	case 400:
		return fallbackOn400
	case 401, 403, 404, 408, 409, 429:
		return true
	}
	return statusCode >= 500
}

// CredentialProblem 这个状态码是不是凭证问题（调用方要给不同的提示）。
func CredentialProblem(statusCode int) bool {
	return statusCode == 401 || statusCode == 403
}
