package app

import (
	"context"
	"testing"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// entryOnly installs nothing but the entry ledger — the one port this package
// itself needs. Any distribution's graph contains one; the kernel's own graph
// contains nothing at all, which is why this test supplies its own.
type entryOnly struct{ table entry.Registry }

func (l entryOnly) Load() ([]modules.Component, error) {
	return []modules.Component{{
		Name:     "entry",
		Type:     "test",
		Provides: []modules.Provision{modules.Provide(entry.Capability, l.table)},
	}}, nil
}

// TestMainExitsWhenNoEntryClaims: nobody claiming the invocation is a human
// sentence and exit 69, not a crash and not 0.
//
// It used to be reachable only by starting a real process (it lived in
// cmd/newgate). It is a plain function call now — the payoff of keeping the
// composition root in the kernel: distributions go through the same path, so the
// two cannot drift into different exit codes.
func TestMainExitsWhenNoEntryClaims(t *testing.T) {
	if code := Main(context.Background(), Options{Loader: entryOnly{table: entry.NewTable()}}); code != 69 {
		t.Errorf("an entry ledger with nothing registered returned %d, want 69", code)
	}
}

// TestMainRunsTheTraceHookAndFailureHook locks the two seams a product plugs in,
// so a distribution's main only has to say "which graph, where the record goes".
func TestMainRunsTheTraceHookAndFailureHook(t *testing.T) {
	traced, stopped := false, false
	failure := error(nil)
	code := Main(context.Background(), Options{
		Loader: entryOnly{table: entry.NewTable()},
		Trace: func() func() {
			traced = true
			return func() { stopped = true }
		},
		OnAssemblyFailure: func(err error) { failure = err },
	})
	if code != 69 {
		t.Fatalf("exit code %d, want 69", code)
	}
	if !traced || !stopped {
		t.Fatalf("trace hook: started=%v stopped=%v — both must happen", traced, stopped)
	}
	if failure != nil {
		t.Fatalf("assembly succeeded but the failure hook got %v", failure)
	}
}
