// Package check is the **static check** for localization: is the ledger stale, do the
// placeholders of the translation and the source match, are there orphan keys, has a machine
// marker been treated as copy, how much Chinese is left in the source.
//
// Like tools/i18n/scan, it is an implementation **shared** by the kernel and the distribution
// (the distribution gets it through replace): the two repositories must use the same ruler,
// otherwise "passes in the distribution, errors in the kernel" becomes routine.
//
// Every check is **offline and free** -- this is what CI runs. The step that calls an LLM
// (translation) is not here, see the sync subcommand in tools/i18n/main.go: that is a
// developer-local task.
package check

import (
	"bytes"
	"encoding/json"
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

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/scan"
)

// Options is the input to one check. The two repositories each pass their own paths.
type Options struct {
	Root       string // 仓库根
	CatalogDir string // 目录文件相对 Root 的位置，如 "lib/i18n/catalogs"
	Allowlist  string // 未迁移文件清单（相对 Root）；空 = 跳过这项检查
	Strict     bool   // 覆盖率不足、机翻未复核是否算失败（发版时用）
}

// Finding is one check result. When Level is "error" the caller should exit non-zero.
type Finding struct {
	Level string // error | warn
	Where string
	Msg   string
}

func (f Finding) String() string {
	where := f.Where
	if where == "" {
		where = "-"
	}
	return fmt.Sprintf("%-5s %s: %s", f.Level, where, f.Msg)
}

// Run runs every check.
func Run(opts Options) ([]Finding, error) {
	var out []Finding
	add := func(level, where, msg string) { out = append(out, Finding{level, where, msg}) }

	calls, err := scan.Dir(opts.Root)
	if err != nil {
		return nil, err
	}
	for _, p := range scan.Problems(calls) {
		add("error", strings.SplitN(p, ":", 2)[0], p)
	}
	msgs := scan.Messages(calls)

	ledgerPath := filepath.Join(opts.Root, opts.CatalogDir, i18n.LedgerName)
	wantLedger := buildLedger(msgs)
	wantRaw, err := i18n.MarshalLedger(wantLedger)
	if err != nil {
		return nil, err
	}
	gotRaw, err := os.ReadFile(ledgerPath)
	if err != nil {
		add("error", ledgerPath, "cannot read the ledger: "+err.Error())
	} else if !bytes.Equal(gotRaw, wantRaw) {
		add("error", ledgerPath, "the ledger is stale: the messages in the source do not match it. "+
			"run `go run ./tools/i18n extract` and commit the result")
	}

	// The arguments given at the call site must cover the placeholders in the message.
	//
	// Why this deserves its own check: a missing argument **does not error**, and the UI just
	// shows a bare `{dir}` forever -- only the user sees it, and the user will not file an issue
	// over it (they assume it is what we meant to show). This is the one i18n failure mode that is
	// "silent enough that nobody comes to fix it", so it is caught at build time.
	//
	// Two **injection** exceptions (written in lib/i18n): N() puts the count into the arguments
	// itself, and Ef() puts the underlying error into {err} -- those two call sites may omit them.
	// Everything else must be given.
	for _, msg := range i18n.SortedKeys(msgs) {
		call := msgs[msg]
		want := i18n.Placeholders(msg)
		if call.Plural != "" {
			want = union(want, i18n.Placeholders(call.Plural))
		}
		have := map[string]bool{}
		for _, a := range call.Args {
			have[a] = true
		}
		if call.Plural != "" {
			have["n"] = true // N() 注入
		}
		if call.Method == "Ef" {
			have["err"] = true // Ef() 注入
		}
		var missing []string
		for _, p := range want {
			if !have[p] {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			add("error", call.Where, fmt.Sprintf(
				"placeholder %v of %q has no matching argument at the call site -- the UI will show {%s} verbatim."+
					" (N() injects n, Ef() injects err, everything else must be written into i18n.A{...})",
				msg, missing, missing[0]))
		}
	}

	// Catalogs: placeholder consistency, no orphan keys, coverage.
	catalogs, err := loadCatalogs(filepath.Join(opts.Root, opts.CatalogDir))
	if err != nil {
		return nil, err
	}
	total := len(msgs)
	for _, cat := range catalogs {
		if cat.Language == i18n.SourceLang {
			add("error", cat.Language, "the source language must not have a translation file -- its text is the sentence in the source")
			continue
		}
		translated := 0
		for _, msg := range i18n.SortedKeys(cat.Messages) {
			e := cat.Messages[msg]
			call, known := msgs[msg]
			if !known {
				add("error", cat.Language, fmt.Sprintf(
					"orphan translation %q: the source no longer has this message (changed the English wording?) -- delete it, or change it back", msg))
				continue
			}
			if e.Empty() {
				continue
			}
			translated++
			if e.Machine && !e.Reviewed && opts.Strict {
				add("error", cat.Language, fmt.Sprintf("machine translation not reviewed: %q", msg))
			}
			want := i18n.Placeholders(msg)
			if call.Plural != "" {
				want = union(want, i18n.Placeholders(call.Plural))
			}
			got := i18n.EntryPlaceholders(e)
			if strings.Join(want, ",") != strings.Join(got, ",") {
				add("error", cat.Language, fmt.Sprintf(
					"placeholders do not match: %q\n      the source wants %v, the translation gives %v (miss one placeholder and the UI will show {name} forever)",
					msg, want, got))
			}
		}
		missing := total - translated
		if missing > 0 {
			level := "warn"
			if opts.Strict {
				level = "error"
			}
			add(level, cat.Language, fmt.Sprintf("%d translations missing (%d in all)", missing, total))
		}
	}

	// Machine markers must not be treated as copy: they are control-flow criteria and protocol
	// names.
	for _, cat := range catalogs {
		for _, msg := range i18n.SortedKeys(cat.Messages) {
			if forbidden[msg] {
				add("error", cat.Language, fmt.Sprintf(
					"%q is a machine marker (a control-flow criterion/protocol name) and must not appear in a catalog", msg))
			}
		}
	}

	if opts.Allowlist != "" {
		findings, err := auditCJK(opts.Root, filepath.Join(opts.Root, opts.Allowlist))
		if err != nil {
			return nil, err
		}
		out = append(out, findings...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out, nil
}

// forbidden lists strings that must **never** enter a catalog -- they look like copy but are
// read by machines.
//
// Structurally they never go through i18n.T at all (`Diagnostic.State` is a switch criterion,
// metric names are protocol fields), so this check guards against **a translator's slip of the
// hand**: someone turns `ok` into a message and changes it to "normal", and that day
// `newgate doctor` 's criterion is broken.
var forbidden = map[string]bool{
	"ok": true, "warn": true, "bad": true, "skip": true,
	"providers.json": true, "state.json": true, "health.json": true,
	"/__newgate/status": true, "/__newgate/metrics": true,
}

// buildLedger turns scan results into a ledger. Human-written notes are kept (extract never
// overwrites something a human wrote).
//
// **No locations** (2026-09-21): `scan.Call` has Where (`file:line`, used to name the spot in
// errors), but the ledger does not keep it -- move a line and the ledger is "stale", and that
// happens every time anyone moves a line of code. The reasoning and the cost are in the Ledger
// comment in lib/i18n.
func buildLedger(msgs map[string]scan.Call) i18n.Ledger {
	led := i18n.Ledger{Messages: make(map[string]i18n.LedgerEntry, len(msgs))}
	for msg, call := range msgs {
		led.Messages[msg] = i18n.LedgerEntry{
			Args:  call.Args,
			Other: call.Plural,
		}
	}
	return led
}

// MergeLedger merges the freshly scanned ledger with the old one: args come from the new,
// **notes from the old**.
func MergeLedger(old, fresh i18n.Ledger) i18n.Ledger {
	out := i18n.Ledger{Messages: make(map[string]i18n.LedgerEntry, len(fresh.Messages))}
	for msg, e := range fresh.Messages {
		if prev, ok := old.Messages[msg]; ok {
			e.Note = prev.Note
		}
		out.Messages[msg] = e
	}
	return out
}

// FreshLedger scans the source to compute the ledger that should exist now (shared by extract
// and check).
func FreshLedger(root, catalogDir string) (i18n.Ledger, error) {
	calls, err := scan.Dir(root)
	if err != nil {
		return i18n.Ledger{}, err
	}
	if problems := scan.Problems(calls); len(problems) > 0 {
		return i18n.Ledger{}, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	old, _ := LoadLedger(filepath.Join(root, catalogDir))
	return MergeLedger(old, buildLedger(scan.Messages(calls))), nil
}

// LoadLedger reads a ledger; when it cannot be read, an empty ledger is returned (the state on
// the first extract run).
func LoadLedger(path string) (i18n.Ledger, error) {
	raw, err := os.ReadFile(filepath.Join(path, i18n.LedgerName))
	if err != nil {
		return i18n.Ledger{Messages: map[string]i18n.LedgerEntry{}}, nil
	}
	return i18n.ParseLedger(raw)
}

// LoadCatalogs reads every catalog under the directory (not the ledger).
func LoadCatalogs(dir string) ([]i18n.Catalog, error) { return loadCatalogs(dir) }

func loadCatalogs(dir string) ([]i18n.Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []i18n.Catalog
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == i18n.LedgerName {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		c, err := i18n.ParseCatalog(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		out = append(out, c)
	}
	return out, nil
}

// ---------- the ratchet on leftover Chinese ----------

// allowlistFile is the "not yet migrated files" list.
//
// It is **migration-period** scaffolding, and its end state is an empty file. Why list files
// rather than literals: migration is done file by file (one module per batch), so listing files
// keeps up with the pace; the cost is that unmigrated files can quietly gain new Chinese -- but
// the list only shrinks, and the end state is "not one character of Chinese in non-test
// source", so this hole has a definite expiry date.
type allowlistFile struct {
	Why   string            `json:"why"`
	Files map[string]string `json:"files"` // 路径 → 为什么还没迁（一句话）
}

// CJKFiles scans for "which non-test source files still have Chinese string literals",
// returning file -> count.
//
// Only **string literals** are looked at: comments are design records and should be Chinese;
// Chinese in test files is fixtures. Neither should be governed by this check. It is exported
// because `audit --seed` in tools/i18n uses it to generate the unmigrated list, and the
// distribution reuses the same criterion.
func CJKFiles(root string) (map[string]int, error) {
	seen := map[string]int{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Which directories to walk, using the **same rule** as scan.Dir (scan.SkipDir): the two
			// criteria must measure the same tree, otherwise you get contradictions like "it is not in
			// the ledger but it is listed in the allowlist" -- and in the distribution the `core/`
			// submodule tree is exactly where that kind of misalignment is most likely.
			if scan.SkipDir(root, p) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") ||
			strings.HasSuffix(p, "_gen.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, p)
		count := 0
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, uerr := strconv.Unquote(lit.Value); uerr == nil && hasCJK(s) {
				count++
			}
			return true
		})
		if count > 0 {
			seen[filepath.ToSlash(rel)] = count
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return seen, nil
}

// auditCJK is that ratchet: the list and the current state must be **exactly equal**.
//
// Both directions must go red. "More than listed" catches newly added Chinese (which should go
// through i18n.T); "fewer than listed" catches "migrated but forgot to remove from the list" --
// checking only one direction would let the list rot, and once rotted it is just a history file
// nobody dares delete.
func auditCJK(root, path string) ([]Finding, error) {
	var out []Finding
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the unmigrated list %s: %w", path, err)
	}
	var al allowlistFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&al); err != nil {
		return nil, fmt.Errorf("cannot decode %s: %w", path, err)
	}
	seen, err := CJKFiles(root)
	if err != nil {
		return nil, err
	}
	for file, n := range seen {
		if _, ok := al.Files[file]; !ok {
			out = append(out, Finding{"error", file, fmt.Sprintf(
				"there are %d Chinese string literals here, but this file is not in the unmigrated list -- user-visible copy must go through "+
					"i18n.T(\"English\", ...); if it really is a machine marker, this check's criterion needs changing", n)})
		}
	}
	for file := range al.Files {
		if _, ok := seen[file]; !ok {
			out = append(out, Finding{"error", file, "the unmigrated list still lists it, but it no longer has any Chinese string literals --" +
				" remove it from the list (the list only shrinks)"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where < out[j].Where })
	return out, nil
}

func hasCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
			return true
		case r >= 0x3000 && r <= 0x303F: // 中文标点（。、：（））
			return true
		case r >= 0xFF00 && r <= 0xFFEF: // 全角标点
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		set[x] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
