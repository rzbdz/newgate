package app

import (
	"os"
	"path/filepath"
	"testing"

	modules "github.com/rzbdz/newgate/component"
)

// The kernel ships **no modules** -- this law is a test today, not a comment.
//
// It is the load-bearing half of the split ("mechanism in the kernel, product in the
// distribution"): the moment a module lives here, the kernel knows a product, and
// "the kernel is a framework" becomes a story rather than a fact. It also keeps the kernel
// buildable offline with nothing to assemble and nothing to serve.
//
// It used to live in selection_test.go, next to the module table that made it checkable that
// way; that file went away with app.Selection and the generated table (2026-11-18), so the
// criterion comes back on its own: **assemble the kernel's own Loader and look at what came
// out**, plus a directory scan for "someone put a module on disk and did not wire it".
func TestKernelShipsNoModules(t *testing.T) {
	comps, err := Loader{}.Load()
	if err != nil {
		t.Fatalf("the kernel's own loader failed: %v", err)
	}
	if len(comps) != 0 {
		t.Fatalf("the kernel's own graph has %d components -- the framework must ship none: %v",
			len(comps), names(comps))
	}

	// And nothing may be sitting in modules/ either: a directory there that is never assembled is
	// the same lie from the other side ("it is in the repository, so it must be installed").
	dir := filepath.Join(repoDir(t), "..", "modules")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// The directory not existing is the healthy state -- just as empty as an empty one.
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("core/modules/%s exists -- the kernel ships no modules (see CLAUDE.md's layering table)", e.Name())
		}
	}
}

func names(comps []modules.Component) []string {
	out := make([]string, 0, len(comps))
	for _, c := range comps {
		out = append(out, c.Name)
	}
	return out
}
