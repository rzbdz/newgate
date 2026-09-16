package probe

import (
	"errors"
	"testing"
	"time"

	"github.com/rzbdz/newgate/go/modules/gateway/dialect"
	"github.com/rzbdz/newgate/go/modules/gateway/quirk"
)

func TestCapabilityCacheSurvivesProcessStateReset(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())
	dialect.Reset()
	quirk.Reset()
	t.Cleanup(func() {
		dialect.Reset()
		quirk.Reset()
	})

	target := Target{Provider: "relay", Model: "model"}
	dialect.Mark(target.Provider, target.Model, dialect.CapOpenAI)
	dialect.MarkUnsupported(target.Provider, target.Model, dialect.CapAnthropic)
	dialect.MarkUnsupported(target.Provider, target.Model, dialect.CapCountTokens)
	quirk.Mark(target.Provider, target.Model, quirk.NoThinkingDisable)

	cache := loadCapabilityCache()
	cache.capture(target)
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}
	dialect.Reset()
	quirk.Reset()

	LoadCachedCapabilities()
	if ok, known := dialect.Supports(target.Provider, target.Model, dialect.CapOpenAI); !ok || !known {
		t.Fatal("支持的方言没有从缓存恢复")
	}
	if ok, known := dialect.Supports(target.Provider, target.Model, dialect.CapAnthropic); ok || !known {
		t.Fatal("不支持的方言没有从缓存恢复")
	}
	if !quirk.Has(target.Provider, target.Model, quirk.NoThinkingDisable) {
		t.Fatal("quirk 没有从缓存恢复")
	}
}

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestTimeoutRetryOnlyRetriesTimeoutOnce(t *testing.T) {
	calls := 0
	status, latency, err := timeoutRetry(func() (int, time.Duration, error) {
		calls++
		if calls == 1 {
			return 0, 12 * time.Second, fakeTimeout{}
		}
		return 200, 2 * time.Second, nil
	})
	if calls != 2 || status != 200 || latency != 2*time.Second || err != nil {
		t.Fatalf("timeout retry = calls:%d status:%d latency:%s err:%v",
			calls, status, latency, err)
	}

	calls = 0
	wantErr := errors.New("connection refused")
	_, _, err = timeoutRetry(func() (int, time.Duration, error) {
		calls++
		return 0, time.Millisecond, wantErr
	})
	if calls != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("non-timeout retried: calls:%d err:%v", calls, err)
	}
}

func TestSummarizeCountsUniqueModels(t *testing.T) {
	results := []Result{
		{Profile: "gpt", Role: "normal", Provider: "p", Model: "terra", OK: false},
		{Profile: "gpt", Role: "mid", Provider: "p", Model: "terra", OK: false},
		{Profile: "gpt", Role: "heavy", Provider: "p", Model: "sol", OK: true},
		{Profile: "gpt", Role: "vision", Provider: "p", Model: "sol", OK: true},
		{Profile: "gpt", Role: "light", Provider: "p", Model: "luna", OK: true},
	}
	summary := Summarize(results)
	if len(summary) != 1 || summary[0].OK != 2 || summary[0].Bad != 1 {
		t.Fatalf("重复档位不应重复计数: %+v", summary)
	}
	if got := UniqueCount(results); got != 3 {
		t.Fatalf("UniqueCount = %d，想要 3", got)
	}
	if got := FailedCount(results); got != 1 {
		t.Fatalf("FailedCount = %d，想要 1", got)
	}
}
