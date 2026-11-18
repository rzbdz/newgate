package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	modscan "github.com/rzbdz/newgate/tools/genmodules/scan"
)

// TestGeneratedModuleListIsCurrent blocks drift like "added a directory under modules/ but
// forgot to regenerate": when the list is stale `make build` regenerates it automatically, but
// people running `go build`/ `go test` directly do not go through the Makefile, and the symptom
// then is a module sitting in the repo that is simply never assembled -- a silent missing
// feature, much harder to find than a compile error, so it fails explicitly here once.
func TestGeneratedModuleListIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires running a subprocess to regenerate; skipped under -short")
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(filepath.Dir(current)) // app/ → go/

	cmd := exec.Command("go", "run", "./tools/genmodules", "-check")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the assembly list is stale, run `make generate`:\n%s", output)
	}

	// Second line of defense: -check compares the generator against its own output, so if the
	// generator logic itself degenerates to empty it would be self-consistent. So also assert here
	// that the list is non-empty and covers every directory under modules/.
	generated := generatedComponents()
	if len(generated) != 0 {
		t.Fatalf("the kernel generated %d components — the framework must ship none", len(generated))
	}
	// The list **is** a snapshot of the directories under modules/ -- the kernel's assembly has
	// only one source (2026-09-20 deleted the second one: the distribution's modules are no longer
	// cloned in by the kernel at build time). Reconcile one by one, so "the module is in the repo
	// but not assembled" goes red right here.
	dirs := moduleDirs(t, filepath.Join(root, "modules"))
	if len(generated) != len(dirs) {
		t.Fatalf("assembled %d components, modules/ has %d directories -- the counts do not match", len(generated), len(dirs))
	}
	for _, dir := range dirs {
		if !strings.Contains(string(mustReadGenerated(t)), "/modules/"+dir+"\"") {
			t.Fatalf("modules/%s is a component, but the generated list has no import for it", dir)
		}
	}
}

// mustReadGenerated reads the generated list source (used for the import reconciliation above).
func mustReadGenerated(t *testing.T) []byte {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), "modules_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// moduleDirs returns every directory name under modules/ that "is a component".
//
// It uses **the generator's own ruler** (tools/genmodules/scan): there used to be two checks
// with different criteria (this one used bytes.Contains to find a literal, the generator used
// the AST to look at the return value), so a module that wrote `func New() component.Component`
// as the docs say was installed by the generator but not counted here, producing a failure that
// pointed nowhere near the real cause (observed 2026-09-18, see the scan package comment).
func moduleDirs(t *testing.T, modulesDir string) []string {
	t.Helper()
	dirs, err := modscan.Dirs(modulesDir)
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}
