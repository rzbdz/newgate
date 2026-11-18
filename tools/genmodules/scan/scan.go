// Package scan is the **only implementation** of the "which directories are components"
// criterion.
//
// Why it is its own package (2026-09-18): there used to be two criteria with different wording
// -- the generator (tools/genmodules) used the AST to see whether the return type was
// `X.Component` , while the app assembly test (moduleDirs in app/modules_gen_test.go) used
// `bytes.Contains` to look for the literal `func New() modules.Component` . The consequence: a
// module written as the docs say, `func New() component.Component` , was installed into the
// list by the generator (the AST recognized it) but not counted in the test's directory set, so
// the test reported "assembled N components, modules/ has M directories" -- a failure that
// points nowhere near the real cause.
//
// Now both call this package. The generator is a **build-time** tool (parse only, no compile),
// the test is an assembly-time assertion, and the two must use the same ruler, otherwise their
// disagreement shows up disguised as some other problem.
package scan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dirs lists every "is a component" directory name under componentsDir, in alphabetical order.
//
// Alphabetical rather than directory-walk order: the generated result must not depend on the
// order the filesystem returns, otherwise diffs wobble across machines.
func Dirs(componentsDir string) ([]string, error) {
	entries, err := os.ReadDir(componentsDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !IsComponent(filepath.Join(componentsDir, entry.Name(), "module.go")) {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out, nil
}

// Ident turns a directory name into a legal Go identifier (the prefix guarantees the first
// character is legal), e.g. `simple-cli` → `mod_simple_cli` .
//
// Why the generator does the mangling instead of requiring the directory to be renamed: a
// directory name **need not be** a Go identifier. The kernel's own modules/ uses underscores
// (claudecode_deepseek), while a distribution repository is another team's directories, where
// hyphenated names like `simple-cli` are perfectly normal. The alias is a generated artifact,
// so the generator should mangle it. Measured 2026-09-20: without mangling it generated
// `ext_simple-cli` -- a syntax error, and one that only shows up at the first compile after
// `make generate` .
//
// Co-located with "which directories are components" (one criterion only): the kernel and the
// distribution each have a generator, and both must use the same ruler, otherwise the same
// directory name would mangle into two different identifiers in the two repositories.
func Ident(prefix, dir string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// isComponent reports whether a module.go exports `func New() modules.Component` . Parse only,
// no compile: this step runs before the build and must not depend on the source type-checking
// at that moment.
func IsComponent(path string) bool {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "New" || fn.Recv != nil {
			continue
		}
		results := fn.Type.Results
		if results == nil || len(results.List) != 1 {
			continue
		}
		sel, ok := results.List[0].Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Component" {
			continue
		}
		return true
	}
	return false
}
