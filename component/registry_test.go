package component

import (
	"errors"
	"strings"
	"testing"
)

// The three invariants of Registry: Release undoes exactly its own entry, a stale Release is
// harmless, and the ledger is unchanged when check rejects.

func TestRegistryReleaseRemovesOnlyItsOwnEntry(t *testing.T) {
	var r Registry[string]

	first, err := r.Register("a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("b", nil); err != nil {
		t.Fatal(err)
	}

	if err := first(); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if got := r.All(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("ledger after releasing a = %v, want [b]", got)
	}
}

// A stale Release returns nil instead of an error, to make a component's Stop idempotent. A
// component's Stop may be called more than once (assembly-failure rollback + normal stop), and
// an extra "it is already gone" on the release path should not be treated as a fault.
func TestRegistryStaleReleaseIsNoop(t *testing.T) {
	var r Registry[string]

	release, err := r.Register("a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("b", nil); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	if err := release(); err != nil {
		t.Errorf("releasing twice should be a harmless no-op, but it errored: %v", err)
	}
	if got := r.All(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("a stale Release touched someone else's entry: %v", got)
	}
}

// check runs under the write lock, so "duplicate check + insert" is atomic; when rejected the
// ledger does not change by a byte.
func TestRegistryCheckRejectionLeavesLedgerUntouched(t *testing.T) {
	var r Registry[string]
	reject := func(existing []string) error {
		for _, v := range existing {
			if v == "dup" {
				return errors.New("dup is already taken")
			}
		}
		return nil
	}

	if _, err := r.Register("dup", reject); err != nil {
		t.Fatalf("the first registration should succeed: %v", err)
	}
	release, err := r.Register("dup", reject)
	if err == nil {
		t.Fatal("the duplicate registration was not stopped by check")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("the error should name the conflicting entry, got: %v", err)
	}
	if release != nil {
		t.Error("when rejected it must not return a Release (the caller would use it to undo an entry that does not exist)")
	}
	if got := r.All(); len(got) != 1 {
		t.Fatalf("ledger after the rejection = %v, want a single entry", got)
	}
}

// check receives a snapshot: modifying the slice passed in must not affect the ledger.
func TestRegistryCheckSeesASnapshot(t *testing.T) {
	var r Registry[string]
	if _, err := r.Register("a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("b", func(existing []string) error {
		if len(existing) != 1 || existing[0] != "a" {
			return errors.New("the existing entries are wrong")
		}
		existing[0] = "tampered"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.All(); len(got) != 2 || got[0] != "a" {
		t.Fatalf("check modified the internal slice: %v", got)
	}
}

func TestRegistryAllIsACopy(t *testing.T) {
	var r Registry[string]
	if _, err := r.Register("a", nil); err != nil {
		t.Fatal(err)
	}
	r.All()[0] = "tampered"
	if got := r.All(); got[0] != "a" {
		t.Fatalf("All returned the internal slice: %v", got)
	}
}

func TestRegistryZeroValueIsUsable(t *testing.T) {
	var r Registry[int]
	if got := r.All(); got != nil {
		t.Fatalf("an empty ledger should return nil, got %v", got)
	}
	if _, err := r.Register(1, nil); err != nil {
		t.Fatalf("the zero value is not usable: %v", err)
	}
	if got := r.All(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("after registering on the zero value = %v", got)
	}
}
