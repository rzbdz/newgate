package main

import (
	"flag"
	"fmt"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/check"
)

// cmdRename moves a translation from the **old wording** to the **new wording**.
//
// # Why it is needed
//
// The rule "key = English source (msgid)" buys "it speaks up when wrong": change the wording
// and the old translation instantly becomes an orphan, `check` reports an error, the UI falls
// back to English -- **there is no translation that "looks fine but is already wrong"**. The
// cost is that changing one sentence means retranslating one sentence. This command pays that
// cost back: when the wording changed but the meaning did not, the translation is **moved
// over**, without spending an LLM call and without producing a "machine translated, unreviewed"
// entry.
//
// This is the same thing as gettext's msgmerge / msgcat: the source changed and the translation
// did not, and the tool carries the translation across instead of making a person retranslate.
// The only difference is that here it is **named explicitly by a human** -- automatic fuzzy
// matching (the most similar orphan) would confuse "one word changed" with "a different
// sentence", and attaching the wrong translation is a silent error, far more expensive than
// typing one more command.
//
// Undo is running it the other way: `rename <new> <old>` (the old one is currently absent from
// the ledger, so the condition holds).
//
// # Criteria
//
//   - The old wording must **already be an orphan** (nobody writes it in the source any more);
//     otherwise it is still in use, and moving it away is deleting a live translation.
//   - The new wording must be in the ledger (the sentence really exists in the source);
//     otherwise it is a typo.
//   - If the new wording already has a translation: reject by default (that usually means you
//     should be translating something else); only `-force` overwrites.
func cmdRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ExitOnError)
	c := commonFlags(fs)
	lang := fs.String("lang", "zh-Hans", "which language's translation")
	force := fs.Bool("force", false, "also overwrite when the new wording already has a translation")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: i18n rename [-lang zh-Hans] <old English text> <new English text>")
	}
	old, fresh := fs.Arg(0), fs.Arg(1)

	led, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	if _, ok := led.Messages[old]; ok {
		return fmt.Errorf("the old wording %q is still used in the source -- moving it away deletes a live translation. "+
			"either change the call sites to the new wording first, or what you actually want is sync", old)
	}
	if _, ok := led.Messages[fresh]; !ok {
		return fmt.Errorf("the new wording %q is not in the ledger -- the source does not contain that sentence (typo? run extract first?)", fresh)
	}

	cat, err := loadOne(c.root, c.catalogDir, *lang)
	if err != nil {
		return err
	}
	entry, ok := cat.Messages[old]
	if !ok || entry.Empty() {
		return fmt.Errorf("%s has no translation for %q -- nothing to move (use sync for a new translation)", *lang, old)
	}
	if prev, exists := cat.Messages[fresh]; exists && !prev.Empty() && !*force {
		return fmt.Errorf("the new wording %q already has a translation (%q) -- add -force to overwrite",
			fresh, firstText(prev))
	}

	delete(cat.Messages, old)
	cat.Messages[fresh] = entry
	if err := writeCatalog(c.root, c.catalogDir, cat); err != nil {
		return err
	}
	mark := ""
	if entry.Machine && !entry.Reviewed {
		mark = "(this one is itself an unreviewed machine translation -- it stays that way after the move)"
	}
	if entry.Machine && entry.Reviewed {
		mark = "(reviewed by a human; it still counts as reviewed after the move)"
	}
	fmt.Printf("i18n: %s: translation moved to the new wording%s\n  old %s\n  new %s\n  translation %s\n",
		*lang, mark, old, fresh, firstText(entry))
	return nil
}

// firstText takes the sentence shown for a translation (for plural entries it takes other --
// Chinese does not distinguish singular and plural).
func firstText(e i18n.Entry) string {
	switch {
	case e.Text != "":
		return e.Text
	case e.Other != "":
		return e.Other
	default:
		return e.One
	}
}
