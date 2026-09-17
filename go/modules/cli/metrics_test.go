package cli

import (
	"testing"

	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
)

func TestModelScoreLineShowsEveryObservedBucket(t *testing.T) {
	status := breakerapi.Status{
		Scores:  [4]int{1200, 3400, 0, 9100},
		Buckets: [4]int{2, 3, 0, 1},
		Samples: 6,
		ScoreMs: 1200,
	}
	got := modelScoreLine(status)
	want := "流畅 · ≤4K 1200ms/2次 · ≤32K 3400ms/3次 · >128K 9100ms/1次"
	if got != want {
		t.Fatalf("modelScoreLine() = %q, want %q", got, want)
	}
}

func TestModelHealthStateSeparatesLaggyAndUnavailable(t *testing.T) {
	laggy := breakerapi.Status{Open: true, Grade: breakerapi.ProbeLaggy}
	if got := modelHealthState(laggy); got != "卡顿" {
		t.Fatalf("laggy state = %q", got)
	}
	unavailable := breakerapi.Status{Open: true, Grade: breakerapi.ProbeUnavailable}
	if got := modelHealthState(unavailable); got != "不可用" {
		t.Fatalf("unavailable state = %q", got)
	}
}

func TestSubMillisecondSampleIsStillScored(t *testing.T) {
	status := breakerapi.Status{Buckets: [4]int{2}, Samples: 2}
	if got := modelHealthState(status); got != "流畅" {
		t.Fatalf("sub-millisecond sample state = %q", got)
	}
	if got := modelScoreLine(status); got != "流畅 · ≤4K <1ms/2次" {
		t.Fatalf("sub-millisecond score = %q", got)
	}
}
