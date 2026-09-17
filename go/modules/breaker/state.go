package breaker

import (
	"strconv"
	"sync"
	"time"
)

type probeResult struct {
	Grade     ProbeGrade
	LatencyMs int64
	CheckedAt time.Time
}

// table 是健康表本体。**一个 binding 一行**（provider + model），不是一 provider
// 一行——同一家上游的不同模型经常一个通一个不通。
//
// 熔断后不会按时间自动复活：只有一次成功 probe 能把 binding 放回链。这是刻意的
// ——坏上游被定时放回来会每分钟撞一次用户请求。代价是「摘了就再也回不来」，等
// 到用户手动 `newgate probe` 为止（2026-09-17 实测 ark 就这样卡了 16 分钟）。
// 半开恢复是紧接着要补的一步，见 docs/05-gateway.md §3。
type table struct {
	mu           sync.Mutex
	fails        map[string]int
	openedAt     map[string]time.Time
	reasons      map[string]string
	probes       map[string]probeResult
	ranker       *ranker
	file         string
	persistedAt  time.Time
	persistTimer *time.Timer
	onError      func(error)
	loadErr      error // 装载期失败，等 SetErrorHandler 装上后补报

	Threshold int           // 真实流量连续失败多少次开闸
	Cooldown  time.Duration // 最短隔离时间；到期仍需成功 probe 才能回链
}

func newTable() *table {
	return &table{
		fails:     map[string]int{},
		openedAt:  map[string]time.Time{},
		reasons:   map[string]string{},
		probes:    map[string]probeResult{},
		ranker:    newRanker(),
		Threshold: 2,
		Cooldown:  60 * time.Second,
	}
}

// SetErrorHandler 注入持久化错误出口；健康状态不能因后台写盘失败而静默丢失。
//
// 装载期的错误（health.json 坏了）也走这里：那一刻还没有出口，所以先存着，
// 出口一装上就立刻补报——「不静默」是这个仓库的硬要求。
func (b *table) SetErrorHandler(fn func(error)) {
	b.mu.Lock()
	b.onError = fn
	pending := b.loadErr
	b.loadErr = nil
	b.mu.Unlock()
	if pending != nil && fn != nil {
		fn(pending)
	}
}

func bindingKey(provider, model string) string { return provider + "\x00" + model }

// Available 只回答 binding 当前是否可进入候选链，不在读路径隐式解除熔断。
func (b *table) Available(provider, model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, open := b.openedAt[bindingKey(provider, model)]
	return !open
}

// RecordSuccess 清零连续失败，但故意不解除熔断；恢复必须由独立 probe 证明。
func (b *table) RecordSuccess(provider, model string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 成功的普通请求只刷新分数，不负责解封。熔断状态必须经过 probe。
	b.fails[bindingKey(provider, model)] = 0
}

// RecordFailure 返回 true 表示这次失败把闸打开了。
func (b *table) RecordFailure(provider, model string) bool {
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
func (b *table) Open(provider, model, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	b.fails[key] = b.Threshold
	b.openedAt[key] = time.Now()
	b.reasons[key] = reason
	b.persistLocked()
}

// RecordProbe 记录主动探活的四档结论，并返回评级和是否熔断。
func (b *table) RecordProbe(provider, model string, status, contextBytes int,
	latency, slowAfter time.Duration, probeErr string) (ProbeGrade, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	grade := ProbeFluent
	reason := ""
	switch {
	case status == 200 && latency > slowAfter:
		grade, reason = ProbeLaggy,
			"probe "+latency.Round(time.Millisecond).String()+" 超过 "+slowAfter.String()
	case probeErr != "":
		grade, reason = ProbeUnavailable, "probe 失败："+probeErr
	case status != 200:
		grade, reason = ProbeUnavailable, "probe HTTP "+strconv.Itoa(status)
	case latency >= 3*time.Second:
		grade = ProbeUsable
	}
	b.probes[key] = probeResult{
		Grade: grade, LatencyMs: latency.Milliseconds(), CheckedAt: time.Now(),
	}
	if latency > 0 {
		b.ranker.observe(key, contextBytes, latency)
	}
	if reason != "" {
		b.fails[key] = b.Threshold
		b.openedAt[key] = time.Now()
		b.reasons[key] = reason
		b.persistLocked()
		return grade, true
	}
	if opened, ok := b.openedAt[key]; ok && time.Since(opened) < b.Cooldown {
		b.persistLocked()
		return grade, true
	}
	b.fails[key] = 0
	delete(b.openedAt, key)
	delete(b.reasons, key)
	b.persistLocked()
	return grade, false
}

// ObserveSuccess 把真实请求的首响应延迟写进对应上下文桶。EWMA 让近期表现
// 权重大，同时避免单次抖动把顺序永久改变。
func (b *table) ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ranker.observe(bindingKey(provider, model), contextBytes, ttft)
	// 延迟样本来自热路径，不能每发请求都落盘；最多每 5 秒写一次。
	// breaker/probe 状态变化仍会立即调用 persistLocked。
	const interval = 5 * time.Second
	if time.Since(b.persistedAt) >= interval {
		b.persistLocked()
	} else if b.file != "" && b.persistTimer == nil {
		wait := interval - time.Since(b.persistedAt)
		b.persistTimer = time.AfterFunc(wait, func() {
			b.mu.Lock()
			b.persistTimer = nil
			b.persistLocked()
			b.mu.Unlock()
		})
	}
}

// Flush 把节流窗口内尚未落盘的延迟样本同步写出，供 daemon 优雅退出使用。
func (b *table) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.persistTimer != nil {
		b.persistTimer.Stop()
		b.persistTimer = nil
	}
	b.persistLocked()
}

// Score 返回当前上下文桶的预测 TTFT（毫秒）。完全未观测用 6s 中性值，
// 排在流畅之后、可用档中部。
func (b *table) Score(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if score, ok := b.ranker.score(bindingKey(provider, model), contextBytes); ok {
		return score
	}
	return 6000
}

// Rank 把延迟档位编码进排序键：流畅最前，未观测居中，可用最后。
func (b *table) Rank(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ranker.rank(bindingKey(provider, model), contextBytes)
}

// Snapshot 冻结一份现状，按 provider/model 排序，便于人读和 diff。
func (b *table) Snapshot() []Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Status, 0, len(b.openedAt))
	for key := range b.entryKeysLocked() {
		provider, model := splitBindingKey(key)
		s := Status{
			Provider: provider,
			Model:    model,
			Fails:    b.fails[key],
			Reason:   b.reasons[key],
		}
		if opened, ok := b.openedAt[key]; ok {
			s.Open = true
			s.OpenFor = time.Since(opened)
			s.OpenedAt = opened
		}
		if p, ok := b.probes[key]; ok {
			s.Grade, s.Latency, s.Checked = p.Grade, p.LatencyMs, p.CheckedAt
		}
		b.ranker.fill(&s, key)
		out = append(out, s)
	}
	sortStatuses(out)
	return out
}

// entryKeysLocked 是「哪些 binding 值得出现在快照/落盘里」的唯一判据。
//
// 曾经是 openedAt ∪ scores ∪ probes——漏掉了「失败过但还没被摘」的 binding，
// 于是 `newgate breaker` 看不见「失败 1 次、闸还没开」，重启也把这个计数丢了。
// 现在并上 fails>0：只要发生过任何一件事，这一行就存在。
func (b *table) entryKeysLocked() map[string]bool {
	keys := map[string]bool{}
	for key := range b.probes {
		keys[key] = true
	}
	for key := range b.openedAt {
		keys[key] = true
	}
	for key := range b.fails {
		if b.fails[key] > 0 {
			keys[key] = true
		}
	}
	for _, key := range b.ranker.keys() {
		keys[key] = true
	}
	return keys
}

func (b *table) reportLocked(err error) {
	if b.onError != nil {
		b.onError(err)
	}
}

func splitBindingKey(key string) (string, string) {
	for i := range key {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// ShouldAdvance 决定这个上游状态码该不该沿链往下走。见 docs/04-configuration.md。
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
func CredentialProblem(statusCode int) bool { return statusCode == 401 || statusCode == 403 }

// Retryable 判断这个失败该不该转移到备用。
// 只转移「确定没产生副作用」的失败——已经开始吐流的绝不转移。
func Retryable(statusCode int, connErr bool) bool {
	return ShouldAdvance(statusCode, false) || connErr
}
