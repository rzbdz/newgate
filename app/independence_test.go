package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The kernel carries no distribution mechanism -- this boundary is a test today, not a comment.
//
// Before 2026-09-20 the kernel had a whole mechanism for "which distribution to install":
// modules-ext.json at the repo root (a Pin), tools/extmanifest (clone the distribution repo by
// Pin), and the -ext half of genmodules. Measured, it bought three costs, and every one of them
// bit someone:
//
//   - Building a distribution once **made the kernel checkout dirty** (the generated assembly
//     list was rewritten with the distribution's version), so `git -C core pull` / switching
//     branches was blocked by the working tree;
//   - The kernel CI had to `git clone` another repository, so the kernel build was no longer
//     offline;
//   - The list imported packages under modules-ext => **a compile-time dependency pointing at
//     the distribution appeared in the kernel**, and "the kernel does not depend on the
//     distribution" is the very definition of this layering.
//
// Now the distribution is a separate Go module that hands its own table to app.Selection (see
// manifest.go). This test pins down "that thing must not come back" -- it governs the
// mechanism, not the words: comments that explain this history may stay (those comments are
// exactly what keeps the next person from re-inventing it).
func TestKernelCarriesNoDistributionMachinery(t *testing.T) {
	// After the flattening (2026-09-20): the Go code is at the repo root, so "module root" and
	// "repo root" are the same directory. Both names are kept -- the one below walks code, the
	// other reports relative paths.
	goDir := filepath.Join(repoDir(t), "..")
	repoRoot := goDir

	// 1. The Pin file itself: it says "which distribution this build uses" -- that is a product
	//    decision.
	pin := filepath.Join(repoRoot, "modules-ext.json")
	if _, err := os.Stat(pin); err == nil {
		t.Errorf("%s has appeared again. The kernel must not declare \"which distribution to use\" -- which modules to install is the distribution's own business, "+
			"it hands the table to the composition root via app.Selection; Pin belongs to the distribution repository", pin)
	}

	// 2. The mechanism in code: import path, package name, two environment variables.
	//
	// Only these three kinds of **code shape** are checked, not the bare string "modules-ext": the
	// directory name of a module checkout appears in the comments telling this history, and that
	// is an asset, not a liability.
	forbidden := []struct{ needle, why string }{
		{`"github.com/rzbdz/newgate/modules-ext`, "the import path of distribution modules -- seeing it in the kernel means the distribution was compiled in at build time again"},
		{"extmanifest.", "that set of tools that pulled the distribution repository by Pin"},
		{"NEWGATE_MODULES_PIN", "the environment variable that overrode the Pin"},
		{"NEWGATE_EXT_DRYRUN", "the environment variable that turned cloning off"},
	}
	err := filepath.WalkDir(goDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The build artifact directory (left over from the old mechanism) holds someone else's code,
			// not the kernel's.
			if d.Name() == "modules-ext" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoRoot, p)
		for _, f := range forbidden {
			if strings.Contains(string(body), f.needle) {
				t.Errorf("%s contains %q: %s", rel, f.needle, f.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", goDir, err)
	}
}
