package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEveryConcreteModuleHasStandardEntryFile(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	modulesDir := filepath.Dir(filepath.Dir(current))
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "builtin" || entry.Name() == "contracts" {
			continue
		}
		path := filepath.Join(modulesDir, entry.Name(), "module.go")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: concrete module must expose root module.go: %v", entry.Name(), err)
		}
	}
}
