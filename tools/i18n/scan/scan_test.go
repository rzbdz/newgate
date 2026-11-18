package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDirStopsAtNestedModule locks down "the scanner does not enter someone else's tree".
//
// Why this matters: the distribution is a separate module, and its `core/` is the kernel
// submodule (carrying its own go.mod). If the scanner walked in, the distribution's ledger
// would suddenly sprout a pile of the kernel's messages -- the symptom is the coverage
// denominator growing for no reason and the allowlist listing `core/...` files (observed once),
// while the ledger itself looks perfectly normal. One tree, two rulers.
func TestDirStopsAtNestedModule(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module ours\n")
	write("ours.go", `package ours

import i18n "github.com/rzbdz/newgate/lib/i18n"

var _ = i18n.T("a message of ours", nil)
`)
	write("core/go.mod", "module theirs\n")
	write("core/theirs.go", `package theirs

import i18n "github.com/rzbdz/newgate/lib/i18n"

var _ = i18n.T("a message of theirs", nil)
`)

	calls, err := Dir(root)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	msgs := Messages(calls)
	if _, ok := msgs["a message of ours"]; !ok {
		t.Error("our own message was not scanned")
	}
	if _, ok := msgs["a message of theirs"]; ok {
		t.Error("it scanned into a nested Go module (someone else's messages entered our ledger)")
	}

	// The same rule must govern the Chinese count too -- the two criteria must measure the same
	// tree.
	if !SkipDir(root, filepath.Join(root, "core")) {
		t.Error("a nested module must be skipped")
	}
	if SkipDir(root, root) {
		t.Error("the root has no exemption outside the go.mod criterion -- skipping it would leave the scan empty")
	}
	if !SkipDir(root, filepath.Join(root, "vendor")) {
		t.Error("vendor must be skipped")
	}
	if got := strings.Join(Problems(calls), "\n"); got != "" {
		t.Errorf("this fixture has no broken call, yet it was reported:\n%s", got)
	}
}
