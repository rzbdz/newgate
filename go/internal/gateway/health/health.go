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

var Default = newBreaker()

func (b *Breaker) SetErrorHandler(fn func(error)) {
	b.mu.Lock()
	b.onError = fn
	b.mu.Unlock()
}

func bindingKey(provider, model string) string { return provider + "\x00" + model }

func (b *Breaker) Available(provider, model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	_, open := b.openedAt[key]
	return !open
}

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

// RecordProbe 记录主动探活的四档结论，并返回评级和是否熔断。
func (b *Breaker) RecordProbe(provider, model string, status, contextBytes int,
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
		b.observeLocked(key, contextBytes, latency)
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
func (b *Breaker) ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observeLocked(bindingKey(provider, model), contextBytes, ttft)
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
func (b *Breaker) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.persistTimer != nil {
		b.persistTimer.Stop()
		b.persistTimer = nil
	}
	b.persistLocked()
}

func (b *Breaker) observeLocked(key string, contextBytes int, ttft time.Duration) {
	bucket := contextBucket(contextBytes)
	s := b.scores[key]
	ms := float64(ttft.Milliseconds())
	if s.Samples[bucket] == 0 {
		s.EWMA[bucket] = ms
	} else {
		const recentWeight = 0.30
		s.EWMA[bucket] = recentWeight*ms + (1-recentWeight)*s.EWMA[bucket]
	}
	s.Samples[bucket]++
	b.scores[key] = s
}

// Score 返回当前上下文桶的预测 TTFT（毫秒）。没有同桶样本时取最近的相邻桶；
// 完全未观测用 6s 中性值，排在流畅之后、可用档中部。
func (b *Breaker) Score(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if score, ok := b.scoreLocked(bindingKey(provider, model), contextBytes); ok {
		return score
	}
	return 6000
}

// Rank 把延迟档位编码进排序键：流畅最前，未观测居中，可用最后。
// 卡顿/不可用正常已被 Available 摘除；若调用方只拿到分数快照，仍沉底。
func (b *Breaker) Rank(provider, model string, contextBytes int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(provider, model)
	ms, known := b.scoreLocked(key, contextBytes)
	if !known {
		return 1_000_000
	}
	switch {
	case ms < 3000:
		return ms
	case ms <= 12000:
		return 2_000_000 + ms
	default:
		return 3_000_000 + ms
	}
}

func (b *Breaker) scoreLocked(key string, contextBytes int) (int, bool) {
	s := b.scores[key]
	bucket := contextBucket(contextBytes)
	if s.Samples[bucket] > 0 {
		return int(s.EWMA[bucket]), true
	}
	for distance := 1; distance < len(s.Samples); distance++ {
		if i := bucket - distance; i >= 0 && s.Samples[i] > 0 {
			return int(s.EWMA[i]), true
		}
		if i := bucket + distance; i < len(s.Samples) && s.Samples[i] > 0 {
			return int(s.EWMA[i]), true
		}
	}
	return 0, false
}

func contextBucket(n int) int {
	switch {
	case n <= 4*1024:
		return 0
	case n <= 32*1024:
		return 1
	case n <= 128*1024:
		return 2
	default:
		return 3
	}
}

type Status struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Fails    int           `json:"fails"`
	Open     bool          `json:"open"`
	OpenFor  time.Duration `json:"open_for_ns"`
	Reason   string        `json:"reason,omitempty"`
	Grade    ProbeGrade    `json:"grade,omitempty"`
	Latency  int64         `json:"latency_ms,omitempty"`
	Checked  time.Time     `json:"checked_at,omitempty"`
	ScoreMs  int           `json:"score_ms"`
	Samples  int           `json:"samples"`
	Scores   [4]int        `json:"scores_ms,omitempty"`
	Buckets  [4]int        `json:"samples_by_bucket,omitempty"`
	OpenedAt time.Time     `json:"opened_at,omitempty"`
}

func (b *Breaker) Snapshot() []Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Status
	keys := map[string]bool{}
	for key := range b.openedAt {
		keys[key] = true
	}
	for key := range b.scores {
		keys[key] = true
	}
	for key := range b.probes {
		keys[key] = true
	}
	for key := range keys {
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
		score := b.scores[key]
		if score.Samples[0] > 0 {
			s.ScoreMs = int(score.EWMA[0])
		}
		for i, n := range score.Samples {
			if n > 0 {
				s.Scores[i] = int(score.EWMA[i])
			}
			s.Buckets[i] = n
			s.Samples += n
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// UseFile 让 daemon 的健康表跨优雅重启存活。只有 daemon 写这个文件；
// probe 通过控制端点提交，避免多进程并发覆盖。
func (b *Breaker) UseFile(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.file = path
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries []Status
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	for _, s := range entries {
		key := bindingKey(s.Provider, s.Model)
		if s.Open {
			b.openedAt[key] = s.OpenedAt
			if b.openedAt[key].IsZero() {
				b.openedAt[key] = time.Now().Add(-b.Cooldown)
			}
			b.reasons[key] = s.Reason
			b.fails[key] = s.Fails
			if b.fails[key] < 1 {
				b.fails[key] = 1
			}
		}
		if s.Grade != "" {
			b.probes[key] = probeResult{
				Grade: s.Grade, LatencyMs: s.Latency, CheckedAt: s.Checked,
			}
		}
		score := b.scores[key]
		for i, n := range s.Buckets {
			if n > 0 {
				score.EWMA[i], score.Samples[i] = float64(s.Scores[i]), n
			}
		}
		// 兼容旧 health.json：当时只保存 ≤4KB 的一个分数。
		if score.Samples[0] == 0 && s.ScoreMs > 0 {
			score.EWMA[0], score.Samples[0] = float64(s.ScoreMs), 1
		}
		if score.Samples != [4]int{} {
			b.scores[key] = score
		}
	}
	return nil
}

func (b *Breaker) persistLocked() {
	if b.file == "" {
		return
	}
	var entries []Status
	keys := map[string]bool{}
	for key := range b.probes {
		keys[key] = true
	}
	for key := range b.openedAt {
		keys[key] = true
	}
	for key := range b.scores {
		keys[key] = true
	}
	for key := range keys {
		provider, model := splitBindingKey(key)
		p := b.probes[key]
		s := Status{
			Provider: provider, Model: model, Fails: b.fails[key],
			Reason: b.reasons[key], Grade: p.Grade, Latency: p.LatencyMs,
			Checked: p.CheckedAt,
		}
		if opened, ok := b.openedAt[key]; ok {
			s.Open, s.OpenedAt, s.OpenFor = true, opened, time.Since(opened)
		}
		score := b.scores[key]
		if score.Samples[0] > 0 {
			s.ScoreMs = int(score.EWMA[0])
		}
		for i, n := range score.Samples {
			if n > 0 {
				s.Scores[i] = int(score.EWMA[i])
			}
			s.Buckets[i] = n
			s.Samples += n
		}
		entries = append(entries, s)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].Model < entries[j].Model
	})
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		b.reportLocked(fmt.Errorf("encode %s: %w", b.file, err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.file), 0o2770); err != nil {
		b.reportLocked(fmt.Errorf("create health directory: %w", err))
		return
	}
	tmp := b.file + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o660); err != nil {
		b.reportLocked(fmt.Errorf("write %s: %w", tmp, err))
		return
	}
	if err := os.Chmod(tmp, 0o660); err != nil {
		b.reportLocked(fmt.Errorf("chmod %s: %w", tmp, err))
		return
	}
	if err := os.Rename(tmp, b.file); err != nil {
		b.reportLocked(fmt.Errorf("replace %s: %w", b.file, err))
		return
	}
	b.persistedAt = time.Now()
}

func (b *Breaker) reportLocked(err error) {
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
