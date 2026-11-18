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
	lang := fs.String("lang", "zh-Hans", "哪门语言的译文")
	force := fs.Bool("force", false, "新措辞已有译文时也覆盖它")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("用法：i18n rename [-lang zh-Hans] <旧英文原文> <新英文原文>")
	}
	old, fresh := fs.Arg(0), fs.Arg(1)

	led, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	if _, ok := led.Messages[old]; ok {
		return fmt.Errorf("旧措辞 %q 还在源码里用着——搬走它等于删掉一条活着的译文。"+
			"要么先把调用点改成新措辞，要么你其实想跑的是 sync", old)
	}
	if _, ok := led.Messages[fresh]; !ok {
		return fmt.Errorf("新措辞 %q 不在账本里——源码里没有这句话（拼错了？先跑 extract？）", fresh)
	}

	cat, err := loadOne(c.root, c.catalogDir, *lang)
	if err != nil {
		return err
	}
	entry, ok := cat.Messages[old]
	if !ok || entry.Empty() {
		return fmt.Errorf("%s 里没有 %q 的译文——没得搬（要新翻就用 sync）", *lang, old)
	}
	if prev, exists := cat.Messages[fresh]; exists && !prev.Empty() && !*force {
		return fmt.Errorf("新措辞 %q 已经有译文了（%q）——要覆盖加 -force",
			fresh, firstText(prev))
	}

	delete(cat.Messages, old)
	cat.Messages[fresh] = entry
	if err := writeCatalog(c.root, c.catalogDir, cat); err != nil {
		return err
	}
	mark := ""
	if entry.Machine && !entry.Reviewed {
		mark = "（这条本身还是机翻未复核——搬过来也还是）"
	}
	if entry.Machine && entry.Reviewed {
		mark = "（人工复核过的，搬过来仍然算复核过）"
	}
	fmt.Printf("i18n: %s: 译文搬到新措辞上%s\n  旧 %s\n  新 %s\n  译文 %s\n",
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
