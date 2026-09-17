package breaker

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBreakerIsScopedToBinding(t *testing.T) {
	b := newTable()
	b.Open("relay", "slow", "probe 太慢")

	if b.Available("relay", "slow") {
		t.Fatal("被 probe 熔断的 binding 仍然可用")
	}
	if !b.Available("relay", "fast") {
		t.Fatal("同 provider 的其他模型被误伤")
	}
	status := b.Snapshot()
	if len(status) != 1 || status[0].Provider != "relay" ||
		status[0].Model != "slow" || status[0].Reason != "probe 太慢" {
		t.Fatalf("熔断快照不对: %+v", status)
	}
}

func TestBreakerCooldownStillRequiresSuccessfulProbe(t *testing.T) {
	b := newTable()
	b.Cooldown = 5 * time.Millisecond
	b.Open("relay", "model", "probe 失败")
	if grade, open := b.RecordProbe("relay", "model", 200, 1,
		time.Millisecond, 12*time.Second, ""); grade != ProbeFluent || !open {
		t.Fatalf("隔离期内 probe 不应提前解封: grade=%s open=%v", grade, open)
	}
	time.Sleep(10 * time.Millisecond)

	if b.Available("relay", "model") {
		t.Fatal("cooldown 到期后未经 probe 就自动回链")
	}
	grade, open := b.RecordProbe("relay", "model", 200, 1,
		time.Second, 12*time.Second, "")
	if grade != ProbeFluent || open {
		t.Fatalf("成功 probe 后未恢复: grade=%s open=%v", grade, open)
	}
	if !b.Available("relay", "model") {
		t.Fatal("成功 probe 后仍未回链")
	}
}

func TestTrafficStillNeedsConsecutiveFailures(t *testing.T) {
	b := newTable()
	if b.RecordFailure("relay", "model") {
		t.Fatal("第一次真实流量失败不应开闸")
	}
	if !b.Available("relay", "model") {
		t.Fatal("第一次失败后 binding 不应被摘除")
	}
	if !b.RecordFailure("relay", "model") {
		t.Fatal("第二次连续失败应开闸")
	}
	if b.Available("relay", "model") {
		t.Fatal("第二次连续失败后 binding 仍然可用")
	}
}

func TestProbeGradesAndContextBuckets(t *testing.T) {
	b := newTable()
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

func TestOpenStateSurvivesRestartAndNeedsProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	first := newTable()
	first.Cooldown = time.Millisecond
	if err := first.UseFile(path); err != nil {
		t.Fatal(err)
	}
	first.Open("relay", "model", "probe 失败")

	second := newTable()
	second.Cooldown = time.Millisecond
	if err := second.UseFile(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if second.Available("relay", "model") {
		t.Fatal("重启并过隔离期后未经 probe 就回链")
	}
	if _, open := second.RecordProbe("relay", "model", 200, 1,
		time.Millisecond, 12*time.Second, ""); open {
		t.Fatal("隔离期后成功 probe 没有解封")
	}

	third := newTable()
	if err := third.UseFile(path); err != nil {
		t.Fatal(err)
	}
	if !third.Available("relay", "model") {
		t.Fatal("probe 解封结果没有持久化")
	}
}

func TestSnapshotReportsEveryContextBucket(t *testing.T) {
	b := newTable()
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
// 这条函数被 forward.go:962 调用来决定"是否要 RecordFailure"。改这个函数
// 等于改了"哪类上游反馈会熔断"，是行权点，**不能**靠推断改——下面的每条
// 都是真上游响应文案的快照或同族。
func TestIsRequestShapeError(t *testing.T) {
	const realReasoning400 = `{"type":"error","message":"The ` + "`reasoning_content`" +
		` in the thinking mode must be passed back to the API. (request id: 202609170712356080642648268d9d67Gf5hQWS)"}`
	const thinkingBlock400 = `{"type":"error","message":"The ` + "`content[].thinking`" +
		` in the thinking mode must be passed back to the API."}`

	tests := []struct {
		name string
		body string
		code int
		want bool
	}{
		// 必须跳过（shape 错误）——
		{"openai dialect reasoning_content 400", realReasoning400, 400, true},
		{"anthropic dialect content[].thinking 400", thinkingBlock400, 400, true},

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
		// 400 但不是 reasoning：换 provider 也修不好，但**仍**要记账——
		// 否则 schema 错的上游（schema 不熟）永远摘不掉。这是反例：
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
