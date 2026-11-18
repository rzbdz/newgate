package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/rzbdz/newgate/tools/i18n/check"
)

// cmdAudit reads/writes the "not yet migrated files" list.
//
// At the start of a migration run `-seed` once to seed it (register every file that has Chinese
// string literals today), and thereafter delete one line from the list each time a file is
// migrated -- the list only shrinks, and the end state is an empty file.
func cmdAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	c := commonFlags(fs)
	seed := fs.Bool("seed", false, "rewrite the unmigrated list from the current state (for the start of a migration)")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	seen, err := check.CJKFiles(c.root)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	if !*seed {
		if len(files) == 0 {
			fmt.Println("i18n: there are no Chinese string literals left in non-test source")
			return nil
		}
		fmt.Printf("i18n: %d files still have Chinese string literals\n", len(files))
		for _, f := range files {
			fmt.Printf("  %-60s %d places\n", f, seen[f])
		}
		return nil
	}

	// Keep the existing note when seeding (a sentence a human wrote must not be erased by a
	// reseed).
	existing := map[string]string{}
	if raw, rerr := os.ReadFile(filepath.Join(c.root, c.allowlist)); rerr == nil {
		var prev struct {
			Files map[string]string `json:"files"`
		}
		if json.Unmarshal(raw, &prev) == nil {
			existing = prev.Files
		}
	}
	out := struct {
		Why   string            `json:"why"`
		Files map[string]string `json:"files"`
	}{
		Why: "Exceptions to the criterion \"no user-visible Chinese string literals in non-test Go source\"." +
			"The criterion guards **user-visible text**, so there are only three kinds of exception, and each must state its own reason: " +
			"(1) the translation layer itself -- it must speak before the catalog is installed and after it breaks; (2) developer tools -- the reader is a developer, " +
			"not a CLI user; (3) test fixtures -- the reader is the person running the tests, and CI logs must stay stable. " +
			"Product source must not appear here: **the list only shrinks**, and every new entry must first answer \"why is this not i18n.T\". " +
			"The \"not migrated yet\" entries left over from the migration will be shrunk away by `audit --seed`.",
		Files: map[string]string{},
	}
	for _, f := range files {
		reason := existing[f]
		if reason == "" {
			reason = "not migrated yet (migration-period exception)"
		}
		out.Files[f] = reason
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(filepath.Join(c.root, c.allowlist)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.root, c.allowlist), raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("i18n: unmigrated list written with %d files -> %s\n", len(files), c.allowlist)
	return nil
}
