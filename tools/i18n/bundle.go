package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ---------- bundle ----------
//
// Compiles catalogs/*.json into a binary the runtime reads directly (see the format comment in
// lib/i18n/bundle.go).
//
// It is a **derivation** step: JSON is the only truth, people edit JSON, tools read and write
// JSON, and this copy can be deleted and regenerated at any time. It is still committed to
// version control so that `-check` can block "edited the JSON, forgot to regenerate" -- the
// same path as manifest/modules_gen.go (generated artifacts in the repo + CI validation).
func cmdBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	c := commonFlags(fs)
	onlyCheck := fs.Bool("check", false, "only check whether bundles are stale, do not write")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	dir := filepath.Join(c.root, c.catalogDir)
	names, err := jsonFileNames(dir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("%s has no .json at all", dir)
	}

	stale := []string{}
	for _, name := range names {
		src := filepath.Join(dir, name)
		raw, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		var out []byte
		if name == i18n.LedgerName {
			led, err := i18n.ParseLedger(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
			out = i18n.EncodeLedger(led)
		} else {
			cat, err := i18n.ParseCatalog(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
			// The file name and meta.language must agree -- CatalogsFromFS has the same check, and the
			// reasoning is there (editing file A while file B is in effect turns into an unsolvable
			// investigation).
			if want := strings.TrimSuffix(name, ".json"); want != cat.Language {
				return fmt.Errorf("%s: meta.language is %q, which does not match the file name", src, cat.Language)
			}
			out = i18n.EncodeCatalog(cat)
		}
		dst := filepath.Join(dir, strings.TrimSuffix(name, ".json")+".bin")
		if *onlyCheck {
			got, rerr := os.ReadFile(dst)
			if rerr != nil || string(got) != string(out) {
				stale = append(stale, filepath.Base(dst))
			}
			continue
		}
		if err := os.WriteFile(dst, out, 0o644); err != nil {
			return err
		}
		fmt.Printf("i18n: %s -> %s (%d bytes)\n", name, filepath.Base(dst), len(out))
	}
	if *onlyCheck {
		if len(stale) > 0 {
			return fmt.Errorf("%s is stale: run `go run ./tools/i18n bundle` and commit the result",
				strings.Join(stale, ", "))
		}
		fmt.Printf("i18n: all %d bundles are current\n", len(names))
	}
	return nil
}

// jsonFileNames lists the .json files in a directory (sorted: directory order depends on the
// filesystem, sorting keeps the result stable).
func jsonFileNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
