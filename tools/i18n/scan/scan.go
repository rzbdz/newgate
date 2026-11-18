// Package scan is the **only implementation** of the "which messages in the source are
// translatable" criterion.
//
// Why it is its own package (same reasoning as tools/genmodules/scan): the kernel and the
// distribution each have a `tools/i18n` (the distribution gets this package through replace),
// and the two must use the same ruler -- otherwise the same call site would count as two
// messages in the two repositories, or be counted in one and not in the other.
//
// The criterion is **key = source-language text** (see the lib/i18n package comment): what is
// scanned is the string literal in calls like `i18n.T("...")` . So there is no "message ID
// table" here, and there cannot be one -- no keys means no such thing as a misspelled key.
package scan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Call is one translatable call.
type Call struct {
	Msg    string   // 消息原文（= 键）
	Method string   // T / N / E / Ef —— 检查实参时要区别对待（N 注入 n、Ef 注入 err）
	Plural string   // N() 的复数形式；空 = 这条不分单复数
	Args   []string // 调用点给的实参名（`i18n.A{"dir": …}` 的键）
	Where  string   // 相对仓库根的 `文件:行`
}

// SkipDir decides which directories in a source tree **should not be entered**.
//
// Two criteria share it: this package's AST scan (which messages) and check.CJKFiles' Chinese
// count (which files are not migrated yet). The two rulers must measure **the same tree** --
// otherwise you get contradictions like "this file is not in the ledger but it is in the
// allowlist", which is the most laborious kind to investigate.
//
// Two kinds of directory are skipped:
//   - artifacts and external code (`.git`/ `bin`/ `dist`/ `vendor`/ `node_modules`);
//   - **nested Go modules** -- the distribution's `core/` is the kernel submodule, which
//     carries its own go.mod and is another product. Scanning into someone else's house would
//     count the kernel's messages in the distribution's ledger. The criterion uses go.mod
//     rather than the directory name: `core` is only today's convention, and "is this place
//     another module" is the thing itself.
func SkipDir(root, path string) bool {
	switch filepath.Base(path) {
	case ".git", "bin", "dist", "node_modules", "vendor":
		return true
	}
	if path != root {
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			return true
		}
	}
	return false
}

// Dir scans one source tree and returns every translatable call (sorted by position).
//
// Only **non-test** source is scanned: literals in tests are fixtures used for assertions, not
// copy shown to users.
func Dir(root string) ([]Call, error) {
	var out []Call
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipDir(root, p) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if strings.HasSuffix(p, "_gen.go") {
			// A generated artifact (an assembly list or the like) holds no user-visible copy; even if it
			// had some, it should be changed on the generator side. Skipped whole, for the same reason
			// app/direction_test.go skips it.
			return nil
		}
		file, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		pkg := i18nAlias(file)
		if pkg == "" {
			return nil // 这个文件没引 lib/i18n
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = p
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			found, cerr := parseCall(fset, call, pkg, rel)
			if cerr != nil {
				out = append(out, Call{Msg: "", Where: cerr.Error()}) // 见下面的约定
				return true
			}
			if found != nil {
				out = append(out, *found)
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Where != out[j].Where {
			return out[i].Where < out[j].Where
		}
		return out[i].Msg < out[j].Msg
	})
	return out, nil
}

// i18nAlias finds the local name of lib/i18n in this file (`i18n` is the convention, but do not
// assume it).
func i18nAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasSuffix(path, "/lib/i18n") {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "i18n"
	}
	return ""
}

// parseCall recognizes one translatable call. A nil return means this call has nothing to do
// with i18n.
func parseCall(fset *token.FileSet, call *ast.CallExpr, pkg, rel string) (*Call, error) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != pkg {
		return nil, nil
	}
	pos := fset.Position(call.Pos())
	where := fmt.Sprintf("%s:%d", rel, pos.Line)

	var msgIdx, pluralIdx int
	switch sel.Sel.Name {
	case "T", "E":
		msgIdx, pluralIdx = 0, -1
	case "Ef": // Ef(err, msg, args)
		msgIdx, pluralIdx = 1, -1
	case "N": // N(msg, plural, n, args)
		msgIdx, pluralIdx = 0, 1
	default:
		return nil, nil
	}
	if len(call.Args) <= msgIdx {
		return nil, fmt.Errorf("%s: %s() was given no message", where, sel.Sel.Name)
	}
	msg, ok := literal(call.Args[msgIdx])
	if !ok {
		return nil, fmt.Errorf("%s: the first argument of %s() is not a string literal --"+
			"the message must be a literal; a concatenated sentence cannot be translated (and should not be: that is data, not copy)",
			where, sel.Sel.Name)
	}
	if strings.TrimSpace(msg) == "" {
		return nil, fmt.Errorf("%s: the message of %s() is an empty string", where, sel.Sel.Name)
	}
	out := &Call{Msg: msg, Method: sel.Sel.Name, Where: where}
	if pluralIdx >= 0 && len(call.Args) > pluralIdx {
		plural, ok := literal(call.Args[pluralIdx])
		if !ok {
			return nil, fmt.Errorf("%s: the plural form of N() is not a string literal", where)
		}
		out.Plural = plural
	}
	// Args: the keys of `i18n.A{"dir": …}` . Take the named keys from the **last** argument -- the
	// contract is "args is the last parameter" (see the A type in lib/i18n).
	for i := len(call.Args) - 1; i >= 0; i-- {
		if keys := compositeKeys(call.Args[i]); keys != nil {
			out.Args = keys
			break
		}
	}
	return out, nil
}

// literal takes a **compile-time constant string**: a literal, or a `+` concatenation of
// literals.
//
// Why concatenation is recognized: a long sentence has to be wrapped in the source, and writing
//
//	i18n.T("the proxy is not running (newgate start) — "+
//		"run `newgate start` first")
//
// is the only way to wrap in Go. Its **value** is one literal, and translating it is no
// different from writing it on one line -- but scanning line by line would misreport this
// completely normal human way of writing as "the message is not a literal". Fold first, judge
// second.
//
// Only literals (including nested concatenations) are folded. Referencing **constant
// identifiers** is not folded: that would need cross-file evaluation, and "the message is at
// the call site, visible" is precisely what msgid-as-key stands on -- if reuse is really
// needed, what should be reused is the translation, not the English source.
func literal(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		left, ok := literal(v.X)
		if !ok {
			return "", false
		}
		right, ok := literal(v.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	}
	return "", false
}

// compositeKeys takes the named keys of a composite literal (`A{"dir": x, "n": y}` → dir, n).
// Returns nil when it is not a composite literal (or uses positional form).
func compositeKeys(e ast.Expr) []string {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil // 位置写法：拿不到名字，交给上层按「没有实参」处理
		}
		key, ok := kv.Key.(*ast.BasicLit)
		if !ok || key.Kind != token.STRING {
			return nil
		}
		s, err := strconv.Unquote(key.Value)
		if err != nil {
			return nil
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Messages folds the scan result into a "message → that call" table. The same message appearing
// in several places is normal (the same sentence used in a few spots); the **first** occurrence
// is taken as the ledger's where.
func Messages(calls []Call) map[string]Call {
	out := make(map[string]Call, len(calls))
	for _, c := range calls {
		if c.Msg == "" {
			continue // 扫描时记下的错误，交给调用方单独报
		}
		if _, seen := out[c.Msg]; !seen {
			out[c.Msg] = c
		}
	}
	return out
}

// Problems extracts the problems recorded during the scan (Msg is empty, the reason is written
// in Where).
func Problems(calls []Call) []string {
	var out []string
	for _, c := range calls {
		if c.Msg == "" {
			out = append(out, c.Where)
		}
	}
	return out
}
