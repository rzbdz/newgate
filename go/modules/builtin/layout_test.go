package builtin

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulesImportPrefix = "github.com/rzbdz/newgate/go/modules/"

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
		if !entry.IsDir() || entry.Name() == "builtin" {
			continue
		}
		if entry.Name() == "contracts" {
			t.Error("capability contracts must be owned by each module's api package")
			continue
		}
		path := filepath.Join(modulesDir, entry.Name(), "module.go")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: concrete module must expose root module.go: %v", entry.Name(), err)
		}
		checkAPIDependencies(t, modulesDir, entry.Name())
	}
}

func checkAPIDependencies(t *testing.T, modulesDir, module string) {
	apiDir := filepath.Join(modulesDir, module, "api")
	files, err := os.ReadDir(apiDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Errorf("%s/api: %v", module, err)
		return
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") {
			continue
		}
		path := filepath.Join(apiDir, file.Name())
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil || !strings.HasPrefix(importPath, modulesImportPrefix) {
				continue
			}
			relative := strings.TrimPrefix(importPath, modulesImportPrefix)
			parts := strings.Split(relative, "/")
			if parts[0] != module && (len(parts) < 2 || parts[1] != "api") {
				t.Errorf("%s imports implementation package %q; cross-module API dependencies must target <module>/api",
					path, importPath)
			}
		}
	}
}
