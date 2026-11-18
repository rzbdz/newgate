package app

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	modscan "github.com/rzbdz/newgate/tools/genmodules/scan"
)

// The kernel ships **no modules** (2026-11-18). These tests therefore exercise the
// Selection contract against a synthetic kernel table: what a distribution relies on
// is the *rules* (names must hit, the root's own port cannot be disabled, duplicate
// names and an empty graph are loud), not today's module list.
//
// The real product graph — "this distribution's spec assembles, the removal matrix
// holds" — lives in the distribution's testing/kernel package.

// TestKernelShipsNoModules is the migration's ratchet: the kernel's own table and
// the disk must both be empty. If someone drops a module back into core/modules,
// this fails instead of quietly reinstalling a product concern into the framework.
func TestKernelShipsNoModules(t *testing.T) {
	if got := CoreModules(); len(got) != 0 {
		t.Fatalf("the kernel ships %d modules (%v) — a module belongs in a distribution", len(got), names(entriesToComponents(got)))
	}
	dirs, err := modscan.Dirs(filepath.Join(repoDir(t), "..", "modules"))
	if err != nil {
		t.Fatalf("scanning core/modules: %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("core/modules has %v — the kernel is a framework, not a product", dirs)
	}
}

// TestKernelLoaderIsEmpty: Loader{} is what the kernel's own binary and `go test`
// see, and it installs nothing.
func TestKernelLoaderIsEmpty(t *testing.T) {
	comps, err := Loader{}.Load()
	if err != nil {
		t.Fatalf("Loader{}.Load(): %v", err)
	}
	if len(comps) != 0 {
		t.Fatalf("Loader{} installed %d components", len(comps))
	}
}

// TestSelectionDisablesByDirName works on a fake kernel table so the rule stays
// testable now that the real table is empty.
func TestSelectionDisablesByDirName(t *testing.T) {
	withCoreModules(t, map[string]modules.Component{"ui": fakeModule("ui"), "data": fakeModule("data")})
	sel, err := Selection{Disable: []string{"ui"}}.Load()
	if err != nil {
		t.Fatalf("disable ui: %v", err)
	}
	if contains(names(sel), "ui") {
		t.Error("disable named ui, but ui is still in the graph")
	}
	if !contains(names(sel), "data") {
		t.Error("disabling one module dropped an unrelated one")
	}
}

// TestSelectionRejectsUnknownNames: every wrong spelling must be loud. The worst
// symptom of a silent one is "I thought I turned it off, it is still running".
func TestSelectionRejectsUnknownNames(t *testing.T) {
	withCoreModules(t, map[string]modules.Component{"data": fakeModule("data"), "entry": rootModule()})
	cases := []struct {
		name string
		sel  Selection
		want string
	}{
		{"a directory that does not exist", Selection{Disable: []string{"nope"}}, "nope"},
		{"a component name instead of a directory name", Selection{Disable: []string{"data-module"}}, "data-module"},
		{"the module serving the root's own port", Selection{Disable: []string{"entry"}}, "root"},
		{
			"a module from Extra (leave it out instead)",
			Selection{Extra: []Entry{{Dir: "probe", Component: helloProbe()}}, Disable: []string{"probe"}},
			"probe",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.sel.Load()
			if err == nil {
				t.Fatalf("no error — the exact silent failure this guards against")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error does not name %q: %v", c.want, err)
			}
		})
	}
}

// TestSelectionRejectsDuplicatesAndEmptyGraph: duplicates, a missing directory name,
// and disabling everything must all fail before anything is assembled.
func TestSelectionRejectsDuplicatesAndEmptyGraph(t *testing.T) {
	withCoreModules(t, map[string]modules.Component{"data": fakeModule("data"), "entry": rootModule()})

	if _, err := (Selection{Extra: []Entry{{Dir: "shadow", Component: fakeModule("data")}}}).Load(); err == nil {
		t.Error("the same component installed twice did not fail")
	}
	if _, err := (Selection{Extra: []Entry{{Dir: "", Component: fakeModule("anon")}}}).Load(); err == nil {
		t.Error("an Extra without a directory name did not fail")
	}

	_, err := Selection{Disable: []string{"data", "entry"}}.Load()
	if err == nil {
		t.Fatal("disabling every module did not fail")
	}
	if !strings.Contains(err.Error(), modules.Name(entry.Capability)) {
		t.Errorf("the error must name the root's own port %q: %v", modules.Name(entry.Capability), err)
	}
}

// TestSelectionAllCoreDisablesEverythingRemovable: AllCore's whole value is that it
// grows with CoreModules() — so the assertion is "what is left is exactly what the
// root itself needs", never a list of names.
func TestSelectionAllCoreDisablesEverythingRemovable(t *testing.T) {
	withCoreModules(t, map[string]modules.Component{"data": fakeModule("data"), "ui": fakeModule("ui"), "entry": rootModule()})
	sel, err := Selection{AllCore: true}.Load()
	if err != nil {
		t.Fatalf("AllCore: %v", err)
	}
	kept := 0
	for _, c := range sel {
		if _, serves := servesCompositionRoot(c); !serves {
			t.Errorf("AllCore left %s in the graph — it is not what the root itself needs", c.Name)
			continue
		}
		kept++
	}
	if kept == 0 {
		t.Error("AllCore also disabled the root's own port")
	}
}

// TestSelectionAllCoreKeepsExtra: AllCore + Extra is the skeleton distribution.
func TestSelectionAllCoreKeepsExtra(t *testing.T) {
	withCoreModules(t, map[string]modules.Component{"data": fakeModule("data"), "entry": rootModule()})
	sel, err := Selection{AllCore: true, Extra: []Entry{{Dir: "probe", Component: helloProbe()}}}.Load()
	if err != nil {
		t.Fatalf("AllCore + Extra: %v", err)
	}
	if !contains(names(sel), "hello-probe") {
		t.Errorf("Extra was not installed: %v", names(sel))
	}
	if contains(names(sel), "data") {
		t.Errorf("AllCore left a module in: %v", names(sel))
	}
}

// TestSelectionRejectsAllCoreWithDisable: both say the same thing; taking the union
// would leave Disable as a stale copy of AllCore, which is what AllCore removed.
func TestSelectionRejectsAllCoreWithDisable(t *testing.T) {
	withCoreModules(t, map[string]modules.Component{"data": fakeModule("data"), "entry": rootModule()})
	_, err := (Selection{AllCore: true, Disable: []string{"data"}}).Load()
	if err == nil {
		t.Fatal("AllCore and Disable together did not fail")
	}
	if !strings.Contains(err.Error(), "AllCore") {
		t.Errorf("the error must say AllCore and Disable collided: %v", err)
	}
}

// TestSelectionAppendsExtraLast: order is not a dependency declaration, but a
// consumer's own modules come after the kernel's so kernel errors surface first.
func TestSelectionAppendsExtraLast(t *testing.T) {
	withCoreModules(t, map[string]modules.Component{"data": fakeModule("data"), "entry": rootModule()})
	sel, err := Selection{Extra: []Entry{{Dir: "extra-probe", Component: helloProbe()}}}.Load()
	if err != nil {
		t.Fatalf("install an extra: %v", err)
	}
	if got := sel[len(sel)-1].Name; got != "hello-probe" {
		t.Errorf("last component is %q, want hello-probe", got)
	}
}

// ---- fakes ----

func fakeModule(name string) modules.Component {
	return modules.Component{Name: name, Type: "test"}
}

// rootModule provides the port the composition root itself needs, under the
// directory name the production entry module uses.
func rootModule() modules.Component {
	return modules.Component{
		Name:     "root",
		Type:     "test",
		Provides: []modules.Provision{modules.Provide(entry.Capability, entry.Registry(entry.NewTable()))},
	}
}

// withCoreModules swaps the kernel's table for the duration of a test.
//
// coreModules is generated and empty; a rule about "how Selection treats a module
// table" is testable only against a table that has modules in it. swapCoreEntries
// is the seam (see manifest.go). Directories are named by the caller so Disable can
// name them.
func withCoreModules(t *testing.T, table map[string]modules.Component) {
	t.Helper()
	dirs := make([]string, 0, len(table))
	for dir := range table {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	entries := make([]Entry, 0, len(dirs))
	for _, dir := range dirs {
		entries = append(entries, Entry{Dir: dir, Component: table[dir]})
	}
	restore := swapCoreEntries(func() []Entry { return entries })
	t.Cleanup(restore)
}

func helloProbe() modules.Component {
	return modules.Component{Name: "hello-probe", Type: "example"}
}

func entriesToComponents(entries []Entry) []modules.Component {
	out := make([]modules.Component, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Component)
	}
	return out
}

func names(components []modules.Component) []string {
	out := make([]string, 0, len(components))
	for _, c := range components {
		out = append(out, c.Name)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
