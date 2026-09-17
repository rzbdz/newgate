package breaker

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBreakerIsScopedToBinding(t *testing.T) {
	b, _ := clocked()
	for i := 0; i < 2; i++ {
		b.Report("relay", "slow", Input{Kind: KindConnError})
	}

	if b.Available("relay", "slow") {
		t.Fatal("被真实流量熔断的 binding 仍然可用")
	}
	if !b.Available("relay", "fast") {
		t.Fatal("同 provider 的其他模型被误伤")
	}
	status := b.Snapshot()
	if len(status) != 1 || status[0].Provider != "relay" || status[0].Model != "slow" {
		t.Fatalf("熔断快照不对: %+v", status)
	}
	if status[0].Rule != "可用性" || status[0].State != "open" {
		t.Fatalf("摘牌原因/状态没进快照: %+v", status[0])
	}
}

// TestSnapshotShowsFailuresBelowThreshold 锁住 2026-09-17 修掉的记账盲区：
// 只有「失败过但还没到阈值」的 binding 曾经对快照和落盘都不可见——
// `newgate breaker` 看不见「失败 1 次、闸还没开」，重启还把这个计数丢掉。
func TestSnapshotShowsFailuresBelowThreshold(t *testing.T) {
	b, _ := clocked()
	b.Report("relay", "model", Input{Kind: KindConnError})

	got := b.Snapshot()
	if len(got) != 1 {
		t.Fatalf("失败 1 次（未开闸）的 binding 没有出现在快照里: %+v", got)
	}
	if got[0].Fails != 1 || got[0].Open || got[0].State != "closed" || got[0].Rule != "可用性" {
		t.Fatalf("未开闸的失败计数不对: %+v", got[0])
	}
}

// TestSnapshotDoesNotInventRowsForNoOps：不记账的结局也不该**造**一行记录。
// 客户端取消和「换谁都一样」的 4xx 每次都会来，给它们各留一行会把
// `newgate breaker` 输出淹掉。
func TestSnapshotDoesNotInventRowsForNoOps(t *testing.T) {
	b, _ := clocked()
	b.Report("relay", "a", Input{Kind: KindClientCancel})
	b.Report("relay", "b", Input{Kind: KindUpstreamStatus, Status: 400, Body: []byte(`{"error":"bad param"}`)})
	b.Report("relay", "c", Input{Kind: KindUpstreamStatus, Status: 402})
	b.Report("relay", "d", Input{Kind: KindUpstreamSuccess})

	if got := b.Snapshot(); len(got) != 0 {
		t.Fatalf("无操作的结局凭空造了记录: %+v", got)
	}
}

func TestProbeGradesAndContextBuckets(t *testing.T) {
	b, _ := clocked()
	if grade, open := b.RecordProbe("relay", "fast", 200, 1,
		2*time.Second, 12*time.Second, ""); grade != ProbeFluent || open {
		t.Fatalf("2s = %s open=%v", grade, open)
	}
	if grade, open := b.RecordProbe("relay", "ok", 200, 1,
		8*time.Second, 12*time.Second, ""); grade != ProbeUsable || open {
		t.Fatalf("8s = %s open=%v", grade, open)
	}
	if grade, open := b.RecordProbe("relay", "slow", 200, 1,
		13*time.Second, 12*time.Second, ""); grade != ProbeLaggy || !open {
		t.Fatalf("13s = %s open=%v", grade, open)
	}

	b.ObserveSuccess("relay", "fast", 64*1024, 20*time.Second)
	if tiny, large := b.Score("relay", "fast", 1), b.Score("relay", "fast", 64*1024); tiny >= large {
		t.Fatalf("上下文分桶没有区分 TTFT: tiny=%d large=%d", tiny, large)
	}
	if fluent, unknown, usable := b.Rank("relay", "fast", 1),
		b.Rank("relay", "unknown", 1), b.Rank("relay", "ok", 1); !(fluent < unknown && unknown < usable) {
		t.Fatalf("评级顺序不对: fluent=%d unknown=%d usable=%d",
			fluent, unknown, usable)
	}
}

// TestOpenStateSurvivesRestart：摘牌状态与冷却到期时刻必须跨优雅重启存活，
// 否则换一次版就等于把所有坏上游偷偷放回来。
func TestOpenStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	first := newTable()
	first.SetPolicy(Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Minute},
	}})
	if err := first.UseFile(path); err != nil {
		t.Fatal(err)
	}
	first.Report("relay", "model", Input{Kind: KindConnError})

	second := newTable()
	if err := second.UseFile(path); err != nil {
		t.Fatal(err)
	}
	if second.Available("relay", "model") {
		t.Fatal("重启后冷却期内的 binding 被放回链上")
	}
	got := second.Snapshot()
	if len(got) != 1 || !got[0].Open || got[0].Fails != 1 ||
		got[0].State != "open" || got[0].Rule != "可用性" {
		t.Fatalf("摘牌状态没有完整恢复: %+v", got)
	}
}

// TestLegacyHealthFileIsReadable：降级回滚用的 known-good 二进制写出的
// health.json 没有 rule / state / open_until 这些新字段，新代码必须照样能读
// ——文件名和键都不许改，就是为了这条。
func TestLegacyHealthFileIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	legacy := `[
  {
    "provider": "relay",
    "model": "old",
    "fails": 2,
    "open": true,
    "open_for_ns": 30000000000,
    "reason": "probe 失败：dial tcp: i/o timeout",
    "grade": "unavailable",
    "latency_ms": 0,
    "checked_at": "2026-09-17T15:40:00Z",
    "opened_at": "2026-09-17T15:40:00Z",
    "score_ms": 1200
  },
  {
    "provider": "relay",
    "model": "bare",
    "fails": 1,
    "open": false
  }
]`
	if err := writeFile(path, legacy); err != nil {
		t.Fatal(err)
	}
	b := newTable()
	if err := b.UseFile(path); err != nil {
		t.Fatal(err)
	}
	if b.Available("relay", "old") {
		t.Fatal("旧文件里的摘牌状态没有被读进来")
	}
	b.mu.Lock()
	openRec := b.records[bindingKey("relay", "old")]
	bareRec := b.records[bindingKey("relay", "bare")]
	b.mu.Unlock()
	// 旧文件没有 rule 字段：那时候只有一本账，就是可用性。
	if openRec == nil || openRec.bucket != BucketAvailability || openRec.fails != 2 {
		t.Fatalf("旧文件的失败计数没有按可用性恢复: %+v", openRec)
	}
	// 只有失败计数、闸没开的行也必须恢复——否则重启白送一次免死金牌。
	if bareRec == nil || bareRec.fails != 1 || !bareRec.openedAt.IsZero() {
		t.Fatalf("旧文件里未开闸的失败计数丢了: %+v", bareRec)
	}
	if got := b.Score("relay", "old", 1024); got != 1200 {
		t.Fatalf("旧文件的 score_ms 没有回填到 4K 档位: %d", got)
	}
}

func TestSnapshotReportsEveryContextBucket(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "model", 1024, 2*time.Second)
	b.ObserveSuccess("relay", "model", 16*1024, 4*time.Second)
	b.ObserveSuccess("relay", "model", 64*1024, 8*time.Second)
	b.ObserveSuccess("relay", "model", 256*1024, 16*time.Second)

	got := b.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot() returned %d bindings", len(got))
	}
	wantScores := [4]int{2000, 4000, 8000, 16000}
	wantSamples := [4]int{1, 1, 1, 1}
	if got[0].Scores != wantScores || got[0].Buckets != wantSamples || got[0].Samples != 4 {
		t.Fatalf("context scores missing from snapshot: %+v", got[0])
	}
}

func TestTrafficScoresSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	first := newTable()
	if err := first.UseFile(path); err != nil {
		t.Fatal(err)
	}
	first.ObserveSuccess("relay", "model", 16*1024, 4*time.Second)
	first.ObserveSuccess("relay", "model", 16*1024, 2*time.Second)
	first.Flush()

	second := newTable()
	if err := second.UseFile(path); err != nil {
		t.Fatal(err)
	}
	got := second.Snapshot()
	if len(got) != 1 || got[0].Scores[1] != 3400 || got[0].Buckets[1] != 2 {
		t.Fatalf("traffic score did not survive restart: %+v", got)
	}
}

// TestIsRequestShapeError 是请求形状错误 vs 可用性错误的策略闸门的回归测试。
//
// 这条函数决定"哪类上游反馈**不**进可用性账本"。改它等于改了"什么错误不该算
// 在这家头上"，是行权点，**不能**靠推断改——下面的每条都是真上游响应文案的
// 快照或同族。
func TestIsRequestShapeError(t *testing.T) {

	tests := []struct {
		name string
		body string
		code int
		want bool
	}{
		// 必须跳过（shape 错误）——
		{"openai dialect reasoning_content 400", reasoning400, 400, true},
		{"anthropic dialect content[].thinking 400", thinking400, 400, true},

		// 必须**不**跳过（真可用性问题）——
		// 401 凭证：绕过去会以为是上游坏，其实是 key 错
		{"401 unauthorized", `{"error":"missing api key"}`, 401, false},
		// 403 权限：同上
		{"403 forbidden", `{"error":"no quota"}`, 403, false},
		// 404 该 provider 没这个模型：换一个能成
		{"404 model not found", `{"error":"model unknown"}`, 404, false},
		// 408/409/429 排队 / 限流 / 冲突：等一下或换一家
		{"408 request timeout", `{"error":"timed out"}`, 408, false},
		{"409 conflict", `{"error":"concurrent edit"}`, 409, false},
		{"429 rate limit", `{"error":"slow down"}`, 429, false},
		// 500/502/503/504 上游挂了：必须熔断
		{"500 server error", `{"error":"internal"}`, 500, false},
		{"502 bad gateway", `{"error":"upstream"}`, 502, false},
		// 400 但不是 reasoning：检测器不认它，于是它既不算形状错（不涨
		// shape_skips）也不进可用性账本——400 一律不记上游的账。这是反例：
		// 400 + "must be passed" + 没 reasoning_content 字符串 → false。
		{"400 unrelated (no reasoning_content)", `{"error":"bad parameter foo"}`, 400, false},
		{"400 'must be passed' without reasoning_content",
			`{"error":"the value must be passed as header"}`, 400, false},

		// 边界：空 body、非 400
		{"empty body", ``, 400, false},
		{"400 with empty body", ``, 400, false},
		{"401 with reasoning_content in body (key phrase elsewhere)",
			`{"error":"unauthorized; see reasoning_content handling"}`, 401, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRequestShapeError([]byte(tt.body), tt.code)
			if got != tt.want {
				t.Errorf("IsRequestShapeError(%q, %d) = %v, want %v",
					tt.body, tt.code, got, tt.want)
			}
		})
	}
}
