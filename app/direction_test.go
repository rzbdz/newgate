package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The composition root **knows no module** -- this law is a test today, not a comment.
//
// The check is mechanical: the non-test sources of `app/*.go` must not contain any import of
// `github.com/rzbdz/newgate/modules/...`, except two places that are **not dependencies**:
//
//   - `modules_gen.go`: the assembly list generated at build time by tools/genmodules. The list
//     has to live somewhere, and the composition root is the only place it belongs (every
//     module it references is named once, and no method is called on any of them).
//   - `modules/config/paths`: a **shared leaf**, which anyone may import directly (spelled out
//     in the layering table of docs/03); it describes "what the configuration looks like on
//     disk", not "how some module works". It is used here to decide which file the assembly log
//     is written to.
//
// # Why it is worth pinning down
//
// Break it and the symptom **appears at compile time, and someone else steps on it**: the user
// removes a module from the list (that is the other half of "installing a module = copying a
// directory"), and the composition root fails to compile because it imports some concrete
// module. This is two sides of the same invariant as "modules are removable" -- see the removal
// matrix in matrix_test.go.
//
// Before 2026-09-20 this was violated in three places, all of them "the composition root
// deciding for modules": `MustGet(pluginmanager).SetCatalog(...)` (the list is now delivered by
// the kernel, see component.CatalogAware), `App.CLI()/Wrapper()` (the entry is now declared by
// modules themselves, see component/entry), `Agent/Slot` type aliases (zero callers, pure
// redundancy).
func TestTheCompositionRootNamesNoModule(t *testing.T) {
	root := repoDir(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读不了 %s: %v", root, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// The whole generated file is skipped: what it lists is every module name, which is **the
		// result of a build-time scan**, a different thing from "the composition root knows a module"
		// (it calls no method).
		if name == "modules_gen.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("读不了 %s: %v", name, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, `"github.com/rzbdz/newgate/modules/`) {
				continue
			}
			// The only exception is the shared leaf config/paths (the layering table of docs/03 says
			// anyone may import it directly): it describes "what the configuration looks like on disk",
			// not a module's internals.
			if strings.Contains(line, "modules/config/paths") {
				continue
			}
			t.Errorf("app/%s:%d 组合根 import 了具体模块——"+
				"摘掉那个模块，组合根就编译不过了：\n    %s\n"+
				"    组合根该做的：装图、把这次调用交给入口。模块自己的知识走\n"+
				"    component/entry（谁是入口）与 component.CatalogAware（图里有谁）。",
				name, i+1, strings.TrimSpace(line))
		}
	}
}

// repoDir is the directory this test file lives in (app/). To get the repo root add one more
// ".." -- before go/ was flattened on 2026-09-20 this was "two levels above the package", and
// that sentence went away with the directory.
func repoDir(t *testing.T) string {
	t.Helper()
	_, this, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 拿不到本文件路径")
	}
	return filepath.Dir(this)
}
