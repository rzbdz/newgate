package health

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBreakerIsScopedToBinding(t *testing.T) {
	b := newBreaker()
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
	b := newBreaker()
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
	b := newBreaker()
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
	b := newBreaker()
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
	first := newBreaker()
	first.Cooldown = time.Millisecond
	if err := first.UseFile(path); err != nil {
		t.Fatal(err)
	}
	first.Open("relay", "model", "probe 失败")

	second := newBreaker()
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

	third := newBreaker()
	if err := third.UseFile(path); err != nil {
		t.Fatal(err)
	}
	if !third.Available("relay", "model") {
		t.Fatal("probe 解封结果没有持久化")
	}
}

func TestSnapshotReportsEveryContextBucket(t *testing.T) {
	b := newBreaker()
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
	first := newBreaker()
	if err := first.UseFile(path); err != nil {
		t.Fatal(err)
	}
	first.ObserveSuccess("relay", "model", 16*1024, 4*time.Second)
	first.ObserveSuccess("relay", "model", 16*1024, 2*time.Second)
	first.Flush()

	second := newBreaker()
	if err := second.UseFile(path); err != nil {
		t.Fatal(err)
	}
	got := second.Snapshot()
	if len(got) != 1 || got[0].Scores[1] != 3400 || got[0].Buckets[1] != 2 {
		t.Fatalf("traffic score did not survive restart: %+v", got)
	}
}
