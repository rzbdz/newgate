package i18n

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// What is embedded is the **bundle (.bin)**, not the JSON: JSON is for people to edit, the
// compile-time copy is for the runtime to read. Every process reads these tables once, and
// parsing 250KB of JSON measured 2.78ms -- nearly half of one `newgate …` assembly (about 6ms).
// The two forms are kept aligned by `tools/i18n bundle`, and `-check` plus the lib/i18n tests
// block "edited the JSON, forgot to regenerate".
//
//go:embed catalogs/*.bin
var catalogsFS embed.FS

// LedgerName is the ledger's file name. It is **not** a language, it is the source-language
// inventory, so it must not follow the `<tag>.json` rule (otherwise it would be treated as a
// language called ledger).
const LedgerName = "ledger.json"

// Builtin reads the kernel's own ledger and catalogs (the copy compiled into the binary).
//
// Why built in: `newgate` is a **single-file static binary** that must be copyable to another
// machine and run directly, without depending on a locale/ directory lying next to it. The disk
// layer (OverlayDir) is an **override**, not the only source.
func Builtin() (Ledger, []Catalog, error) {
	led, err := ledgerFromBundle(catalogsFS, "catalogs")
	if err != nil {
		return Ledger{}, nil, err
	}
	cats, err := BundlesFromFS(catalogsFS, "catalogs")
	if err != nil {
		return Ledger{}, nil, err
	}
	return led, cats, nil
}

// BundlesFromFS reads every `<tag>.bin` from a directory (the compile-time generated copy, see
// bundle.go).
//
// Together with CatalogsFromFS these are two inputs for the same thing: that one reads JSON
// (written by people, written by tools, used for disk overrides), this one reads bundles (used
// at runtime). **They must be two implementations rather than "pick one automatically"**:
// automatic picking would give a luck-dependent result when "both are present in the
// directory", and that indeterminacy is exactly the hardest form of "edited the JSON, forgot to
// regenerate" to investigate.
func BundlesFromFS(fsys fs.FS, dir string) ([]Catalog, error) {
	names, err := bundleNames(fsys, dir)
	if err != nil {
		return nil, err
	}
	out := make([]Catalog, 0, len(names))
	for _, name := range names {
		if name == bundleLedgerName {
			continue
		}
		raw, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		c, err := DecodeCatalog(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, name, err)
		}
		// The file name and meta.language must agree (same reasoning as the one in CatalogsFromFS).
		if want := strings.TrimSuffix(name, ".bin"); want != c.Language {
			return nil, fmt.Errorf("%s/%s: language is %q, which disagrees with the file name",
				dir, name, c.Language)
		}
		out = append(out, c)
	}
	return out, nil
}

// LedgerBundleName is the bundle file name of the ledger (the compile-time form of LedgerName).
const LedgerBundleName = "ledger.bin"

const bundleLedgerName = LedgerBundleName

func ledgerFromBundle(fsys fs.FS, dir string) (Ledger, error) {
	raw, err := fs.ReadFile(fsys, dir+"/"+bundleLedgerName)
	if err != nil {
		return Ledger{}, fmt.Errorf("cannot read the ledger bundle %s/%s: %w", dir, bundleLedgerName, err)
	}
	led, err := DecodeLedger(raw)
	if err != nil {
		return Ledger{}, fmt.Errorf("%s/%s: %w", dir, bundleLedgerName, err)
	}
	return led, nil
}

func bundleNames(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bin") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// CatalogsFromFS reads every `<tag>.json` catalog from a directory (`ledger.json` does not
// count).
//
// A distribution module bringing its own copy also uses it: the distribution is a separate Go
// module, the kernel does not know its directory names, so "embed your own tables and register
// them" has to be done by the distribution itself.
func CatalogsFromFS(fsys fs.FS, dir string) ([]Catalog, error) {
	names, err := jsonNames(fsys, dir)
	if err != nil {
		return nil, err
	}
	out := make([]Catalog, 0, len(names))
	for _, name := range names {
		if name == LedgerName {
			continue
		}
		raw, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		c, err := ParseCatalog(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, name, err)
		}
		// The file name and meta.language must agree: when they disagree, "why did my edit to
		// zh-Hans.json not take effect" turns into an unsolvable investigation (you edited file A,
		// file B is the one in effect).
		if want := strings.TrimSuffix(name, ".json"); want != c.Language {
			return nil, fmt.Errorf("%s/%s: meta.language is %q, which disagrees with the file name",
				dir, name, c.Language)
		}
		out = append(out, c)
	}
	return out, nil
}

// ledgerFromFS reads the ledger.
func ledgerFromFS(fsys fs.FS, dir string) (Ledger, error) {
	raw, err := fs.ReadFile(fsys, dir+"/"+LedgerName)
	if err != nil {
		return Ledger{}, fmt.Errorf("cannot read the ledger %s/%s: %w", dir, LedgerName, err)
	}
	led, err := ParseLedger(raw)
	if err != nil {
		return Ledger{}, fmt.Errorf("%s/%s: %w", dir, LedgerName, err)
	}
	return led, nil
}

func jsonNames(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 目录序与文件系统有关，排序保证结果稳定
	return names, nil
}
