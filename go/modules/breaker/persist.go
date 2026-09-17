package breaker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// UseFile 让 daemon 的健康表跨优雅重启存活。只有 daemon 写这个文件；
// 探活结论由 CLI 通过控制端点提交，避免多进程并发覆盖。
//
// 文件名保持 `health.json`（paths.HealthFile）**刻意不改**：降级回滚用的
// known-good 二进制读的就是这个名字，改名等于回滚即丢状态。
func (b *table) UseFile(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.file = path
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		b.loadErr = fmt.Errorf("读 %s: %w", path, err)
		return b.loadErr
	}
	var entries []Status
	if err := json.Unmarshal(raw, &entries); err != nil {
		b.loadErr = fmt.Errorf("解析 %s: %w", path, err)
		return b.loadErr
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
		} else if s.Fails > 0 {
			// 之前只有「已摘牌」的计数会被恢复；连锁未开的失败计数丢了，
			// 于是重启等于偷偷给每条 binding 一次免死金牌。
			b.fails[key] = s.Fails
		}
		if s.Grade != "" {
			b.probes[key] = probeResult{
				Grade: s.Grade, LatencyMs: s.Latency, CheckedAt: s.Checked,
			}
		}
		score := latencyScore{}
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
			b.ranker.set(key, score)
		}
	}
	return nil
}

func (b *table) persistLocked() {
	if b.file == "" {
		return
	}
	keys := b.entryKeysLocked()
	entries := make([]Status, 0, len(keys))
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
		b.ranker.fill(&s, key)
		entries = append(entries, s)
	}
	sortStatuses(entries)
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

func sortStatuses(entries []Status) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].Model < entries[j].Model
	})
}
