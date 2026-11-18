// Command i18n is the localization tool: scan the source to generate the ledger, run static
// checks, look at coverage, and **locally** fill in missing translations (by calling your own
// gateway to run an LLM).
//
// The subcommands fall into two groups, do not mix them up:
//
//	offline, free (run in CI)   extract / check / status / missing
//	changed wording, do not want to retranslate  rename
//	calls an LLM (**developer-local only**)  sync
//
// The rule the user set: translation never enters any automated flow. CI only runs the first
// group -- is the ledger stale, do the placeholders match, are there orphan keys, how much
// Chinese is left in the source. The step that costs money is always typed by a human on
// purpose.
//
// The distribution repository has a shell of the same name (tools/i18n/main.go) that reuses the
// scan/check packages here (getting the kernel through replace) -- the two repositories must
// judge by the same ruler.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/check"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "extract":
		err = cmdExtract(args)
	case "bundle":
		err = cmdBundle(args)
	case "check":
		err = cmdCheck(args)
	case "status":
		err = cmdStatus(args)
	case "audit":
		err = cmdAudit(args)
	case "missing":
		err = cmdMissing(args)
	case "rename":
		err = cmdRename(args)
	case "sync":
		err = cmdSync(args)
	case "translate":
		err = cmdTranslate(args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown subcommand %q (see `i18n help`)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "i18n:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`Usage: go run ./tools/i18n <subcommand> [options]

  extract [-check]      rebuild the ledger by scanning the source (-check only verifies staleness, writes no file)
  check [-strict]       static checks: ledger / placeholders / orphan keys / coverage / leftover Chinese
  bundle [-check]       compile catalogs/*.json into a .bin read at runtime (-check: staleness only)
  status                coverage and machine-translation counts per language
  audit [-seed]         read/write the "not yet migrated files" list (-seed seeds it)
  missing [-lang zh-Hans]  list the missing translations
  rename <old> <new>    move a translation from old wording to new (same meaning; no LLM)
  sync [-lang zh-Hans]  fill in missing translations (LLM via the local gateway, **local**)
  translate             same as sync, an alias
`)
}

// ---------- common options ----------

type common struct {
	root       string
	catalogDir string
	allowlist  string
}

func commonFlags(fs *flag.FlagSet) *common {
	c := &common{}
	fs.StringVar(&c.root, "root", "", "repository root (defaults to walking up to go.mod)")
	fs.StringVar(&c.catalogDir, "catalogs", "lib/i18n/catalogs", "directory holding the catalog files (relative to the repository root)")
	fs.StringVar(&c.allowlist, "allowlist", "tools/i18n/allowlist.json", "unmigrated list (relative to the repository root, empty = no check)")
	return c
}

func (c *common) resolve() error {
	if c.root != "" {
		return nil
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	c.root = root
	return nil
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found walking up (starting from %s)", dir)
		}
		dir = parent
	}
}

// ---------- extract ----------

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	c := commonFlags(fs)
	onlyCheck := fs.Bool("check", false, "only verify the ledger is not stale, write no file")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}

	fresh, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	raw, err := i18n.MarshalLedger(fresh)
	if err != nil {
		return err
	}
	path := filepath.Join(c.root, c.catalogDir, i18n.LedgerName)
	if *onlyCheck {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != string(raw) {
			return fmt.Errorf("%s is stale: run `go run ./tools/i18n extract` and commit the result", path)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("i18n: ledger %d entries -> %s\n", len(fresh.Messages), path)
	return nil
}

// ---------- check ----------

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	c := commonFlags(fs)
	strict := fs.Bool("strict", false, "low coverage and unreviewed machine translations also fail (use before a release)")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}

	findings, err := check.Run(check.Options{
		Root: c.root, CatalogDir: c.catalogDir, Allowlist: c.allowlist, Strict: *strict,
	})
	if err != nil {
		return err
	}
	errs, warns := 0, 0
	for _, f := range findings {
		fmt.Println(f.String())
		if f.Level == "error" {
			errs++
		} else {
			warns++
		}
	}
	if errs > 0 {
		return fmt.Errorf("%d errors, %d warnings", errs, warns)
	}
	if warns > 0 {
		fmt.Printf("i18n: 0 errors, %d warnings\n", warns)
		return nil
	}
	fmt.Println("i18n: all checks passed")
	return nil
}

// ---------- status ----------

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	c := commonFlags(fs)
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	fresh, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	cats, err := check.LoadCatalogs(filepath.Join(c.root, c.catalogDir))
	if err != nil {
		return err
	}
	total := len(fresh.Messages)
	fmt.Printf("%-12s %6s %6s %8s %8s\n", "LANGUAGE", "TOTAL", "DONE", "MACHINE", "MISSING")
	rows := []string{i18n.SourceLang}
	for _, cat := range cats {
		rows = append(rows, cat.Language)
	}
	sort.Strings(rows)
	for _, lang := range rows {
		done, machine := 0, 0
		var messages map[string]i18n.Entry
		for _, cat := range cats {
			if cat.Language == lang {
				messages = cat.Messages
			}
		}
		if lang == i18n.SourceLang {
			done = total
		} else {
			for msg, e := range messages {
				if _, known := fresh.Messages[msg]; !known || e.Empty() {
					continue
				}
				done++
				if e.Machine && !e.Reviewed {
					machine++
				}
			}
		}
		fmt.Printf("%-12s %6d %6d %8d %8d\n", lang, total, done, machine, total-done)
	}
	return nil
}

// ---------- missing ----------

func cmdMissing(args []string) error {
	fs := flag.NewFlagSet("missing", flag.ExitOnError)
	c := commonFlags(fs)
	lang := fs.String("lang", "zh-Hans", "which language to look at")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	fresh, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	cats, err := check.LoadCatalogs(filepath.Join(c.root, c.catalogDir))
	if err != nil {
		return err
	}
	have := map[string]i18n.Entry{}
	for _, cat := range cats {
		if cat.Language == *lang {
			have = cat.Messages
		}
	}
	n := 0
	for _, msg := range i18n.SortedKeys(fresh.Messages) {
		if e, ok := have[msg]; ok && !e.Empty() {
			continue
		}
		fmt.Println(msg)
		n++
	}
	fmt.Fprintf(os.Stderr, "i18n: %s is missing %d\n", *lang, n)
	return nil
}

// catalogPath is the catalog file for one language.
func catalogPath(root, catalogDir, lang string) string {
	return filepath.Join(root, catalogDir, lang+".json")
}

// loadOne reads one language's catalog (when absent, returns an empty table carrying meta).
func loadOne(root, catalogDir, lang string) (i18n.Catalog, error) {
	raw, err := os.ReadFile(catalogPath(root, catalogDir, lang))
	if err != nil {
		if !os.IsNotExist(err) {
			return i18n.Catalog{}, err
		}
		return i18n.Catalog{
			Language: lang,
			Source:   i18n.SourceLang,
			Widths:   map[string]int{"label": 8, "term": 12},
			Messages: map[string]i18n.Entry{},
		}, nil
	}
	return i18n.ParseCatalog(raw)
}

func writeCatalog(root, catalogDir string, cat i18n.Catalog) error {
	raw, err := i18n.MarshalCatalog(cat)
	if err != nil {
		return err
	}
	path := catalogPath(root, catalogDir, cat.Language)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Write a temp file and then rename: writing half a file makes the next parse fail, and the
	// scene of that failure is a JSON file thousands of lines long -- far more expensive than one
	// rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
