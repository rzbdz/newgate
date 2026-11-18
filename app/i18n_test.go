package app

import (
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/tools/i18n/check"
)

// TestLocalizationStaysConsistent is the **second line of defense** for `tools/i18n check`.
//
// The check has only one implementation (tools/i18n/check): CI runs the same command in another
// step, and that step blocks "forgot to run it"; this one blocks "ran it but nobody looked at
// the output" -- it makes local `go test ./...` raise problems like "the ledger is stale /
// placeholders do not line up / orphan translations / a Chinese string literal has appeared in
// the source again". The same approach as app/modules_gen_test.go takes for genmodules.
//
// The rule set when i18n was added on 2026-09-20: **user-visible text in non-test Go source
// always goes through i18n.T("English", ...)**, and Chinese lives only in the translation
// tables. This ratchet is the enforcer of that sentence.
func TestLocalizationStaysConsistent(t *testing.T) {
	root := filepath.Join(repoDir(t), "..")
	findings, err := check.Run(check.Options{
		Root:       root,
		CatalogDir: "lib/i18n/catalogs",
		Allowlist:  "tools/i18n/allowlist.json",
	})
	if err != nil {
		t.Fatalf("本地化检查跑不起来: %v", err)
	}
	for _, f := range findings {
		if f.Level == "error" {
			t.Errorf("%s", f.String())
		}
	}

	// Degenerate guard: when the check itself breaks it must go red, instead of "reports nothing"
	// looking like a pass. The whole value of this ratchet is that "it speaks"; a muted check is
	// worse than no check.
	cats, cerr := check.LoadCatalogs(filepath.Join(root, "lib/i18n/catalogs"))
	if cerr != nil {
		t.Fatalf("读不了译文目录——判据退化了: %v", cerr)
	}
	if len(cats) == 0 {
		t.Fatal("一份译文都没有——判据退化了（目录挪了？catalogs 里只剩账本？）")
	}
	led, lerr := check.LoadLedger(filepath.Join(root, "lib/i18n/catalogs"))
	if lerr != nil {
		t.Fatalf("账本读不出来——判据退化了: %v", lerr)
	}
	if len(led.Messages) == 0 {
		t.Fatal("账本是空的——扫描器坏了，或者源码里一条 i18n.T 都没有了")
	}
}
