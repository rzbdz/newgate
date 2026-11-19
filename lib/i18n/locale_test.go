package i18n

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests build their own ledger and catalogs and do not depend on the two in catalogs/ --
// those grow continuously as migration proceeds, and using them as fixtures would mean touching
// the tests for every module migrated.
func testLedger() Ledger {
	return Ledger{Messages: map[string]LedgerEntry{
		"Proxy": {},
		"An external file {names} is still in {dir}": {
			Args: []string{"dir", "names"}},
		"Saved {n} file": {
			Args: []string{"n"}, Other: "Saved {n} files"},
		"Only in the source language": {},
	}}
}

func testZh() Catalog {
	return Catalog{
		Language: "zh-Hans",
		Source:   SourceLang,
		Widths:   map[string]int{"label": 8},
		Messages: map[string]Entry{
			"Proxy": {Text: "代理"},
			"An external file {names} is still in {dir}": {Text: "同名外部文件仍在 {dir}：{names}"},
			"Saved {n} file": {Other: "已保存 {n} 个文件"}, // Chinese does not inflect for number
			// "Only in the source language" is deliberately not translated: verifies the fallback
		},
	}
}

func testEn() Catalog {
	return Catalog{Language: SourceLang, Widths: map[string]int{"label": 10}}
}

func install(t *testing.T, tag string, extra ...Catalog) {
	t.Helper()
	cats := append([]Catalog{testEn(), testZh()}, extra...)
	if _, err := Install(tag, testLedger(), cats, ""); err != nil {
		t.Fatalf("Install(%q): %v", tag, err)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"zh_CN.UTF-8": "zh-CN",
		"zh-TW":       "zh-TW",
		"en_US.utf8":  "en-US",
		"zh":          "zh",
		"C":           "",
		"POSIX":       "",
		"":            "",
		"  ":          "",
		"sr_RS@latin": "sr-RS",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	avail := []string{"en", "zh-Hans", "zh-Hant"}
	cases := map[string]string{
		"zh-CN":       "zh-Hans",
		"zh_CN.UTF-8": "zh-Hans",
		"zh-SG":       "zh-Hans",
		"zh-TW":       "zh-Hant",
		"zh-HK":       "zh-Hant",
		"zh-Hans":     "zh-Hans",
		"zh":          "zh-Hans", // a bare zh means simplified
		"zh-Hant-HK":  "zh-Hant",
		"en-US":       "en",
		"en":          "en",
		"fr-FR":       "", // no French -> the layer above falls back to the source
		"C":           "",
		"":            "",
	}
	for in, want := range cases {
		if got := Match(in, avail); got != want {
			t.Errorf("Match(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSourceLanguageIsIdentity(t *testing.T) {
	install(t, "en")
	// Source language path: the sentence in the code is the final text, not a character of catalog
	// data is used
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("the source language must be identity: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 3, nil); got != "Saved 3 files" {
		t.Errorf("source-language plural: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 1, nil); got != "Saved 1 file" {
		t.Errorf("source-language singular: %q", got)
	}
	// A sentence that is not in the catalog at all still comes out as is -- there is no such thing
	// as a "key", so there is no misspelled key
	if got := T("Never listed anywhere", nil); got != "Never listed anywhere" {
		t.Errorf("an unregistered message must come out as is: %q", got)
	}
}

func TestTranslationAndFallback(t *testing.T) {
	install(t, "zh-Hans")
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("the Chinese table did not take effect: %q", got)
	}
	// Missing translation -> falls back to the English sentence in the source (not the key name,
	// because the key is that sentence)
	if got := T("Only in the source language", nil); got != "Only in the source language" {
		t.Errorf("a missing translation must fall back to the source language: %q", got)
	}
}

func TestPlaceholders(t *testing.T) {
	install(t, "zh-Hans")
	got := T("An external file {names} is still in {dir}", A{"dir": "/usr/local/bin", "names": "claude"})
	want := "同名外部文件仍在 /usr/local/bin：claude" // word order differs from English; only named placeholders allow it
	if got != want {
		t.Errorf("named placeholder substituted wrong:\n got %q\nwant %q", got, want)
	}
	// One argument too few: that placeholder is **left as is** (pinpointing what is missing here),
	// not silently emptied
	got = T("An external file {names} is still in {dir}", A{"dir": "/x"})
	if !strings.Contains(got, "{names}") {
		t.Errorf("a missing argument must leave the placeholder in place, got %q", got)
	}
}

func TestPlural(t *testing.T) {
	install(t, "zh-Hans")
	if got := N("Saved {n} file", "Saved {n} files", 1, nil); got != "已保存 1 个文件" {
		t.Errorf("Chinese does not distinguish singular and plural: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 5, nil); got != "已保存 5 个文件" {
		t.Errorf("Chinese does not distinguish singular and plural: %q", got)
	}
	// The n given at the call site and the {n} in the translation are the same one (withN fills it
	// in)
	if got := N("Saved {n} file", "Saved {n} files", 1, A{"n": 7}); got != "已保存 7 个文件" {
		t.Errorf("an explicitly given n must win: %q", got)
	}
}

// A translation without plural forms still gets the count: the catalog only needs `other` for
// Chinese, and that entry is `Text`, not `Other` - so this is the shape every translated
// i18n.N(...) takes in practice. Before 2026-11-19 this branch skipped withN and the rendered
// line carried a literal `{n}` (seen live: "backfilled … on {n} assistant messages").
func TestPluralWithASingleFormTranslation(t *testing.T) {
	install(t, "zh-Hans", Catalog{
		Language: "zh-Hans",
		Source:   SourceLang,
		Messages: map[string]Entry{
			"Saved {n} file": {Text: "已保存 {n} 个文件"},
		},
	})
	if got := N("Saved {n} file", "Saved {n} files", 3, nil); got != "已保存 3 个文件" {
		t.Errorf("a single-form translation must still substitute {n}: %q", got)
	}
	// T() goes through the same branch with no count at all; it must not invent an `n`.
	if got := T("Saved {n} file", A{"n": 9}); got != "已保存 9 个文件" {
		t.Errorf("an explicitly given n must still win under T(): %q", got)
	}
}

func TestEnglishPluralForms(t *testing.T) {
	// When English appears as a translation (say an English user switches the language to en-GB,
	// or someone makes a revised table for English)
	install(t, "en-GB", Catalog{
		Language: "en-GB",
		Messages: map[string]Entry{
			"Saved {n} file": {One: "Saved {n} file", Other: "Saved {n} files"},
		},
	})
	if got := N("Saved {n} file", "Saved {n} files", 1, nil); got != "Saved 1 file" {
		t.Errorf("English singular: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 0, nil); got != "Saved 0 files" {
		t.Errorf("English 0 uses the plural: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 2, nil); got != "Saved 2 files" {
		t.Errorf("English plural: %q", got)
	}
}

func TestRequestedLanguageFallsBackToSource(t *testing.T) {
	// Requesting an untranslated language: no error, no refusal to start, the source language is
	// used silently (visible in Info/Missing)
	eff, err := Install("fr-FR", testLedger(), []Catalog{testEn(), testZh()}, "")
	if err != nil {
		t.Fatal(err)
	}
	if eff != SourceLang {
		t.Fatalf("must fall back to the source language, got %q", eff)
	}
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("after the fallback it must be English: %q", got)
	}
}

func TestAvailableAndMissing(t *testing.T) {
	install(t, "en")
	var zhInfo Info
	for _, i := range Available() {
		if i.Language == "zh-Hans" {
			zhInfo = i
		}
	}
	if zhInfo.Total != 4 {
		t.Errorf("the ledger has 4 entries in all, reported %d", zhInfo.Total)
	}
	if zhInfo.Translated != 3 {
		t.Errorf("3 entries are translated into Chinese, reported %d", zhInfo.Translated)
	}
	miss := Missing("zh-Hans")
	if len(miss) != 1 || miss[0] != "Only in the source language" {
		t.Errorf("a missing message reported an error: %v", miss)
	}
	if len(Missing(SourceLang)) != 0 {
		t.Error("the source language must not be missing any message")
	}
	// The source language is always in the available list (it needs no catalog file)
	var sawSource bool
	for _, i := range Available() {
		if i.Language == SourceLang {
			sawSource = true
		}
	}
	if !sawSource {
		t.Error("the source language must be in the available list")
	}
}

func TestMachineAndReviewedAreCounted(t *testing.T) {
	de := Catalog{
		Language: "de",
		Messages: map[string]Entry{
			"Proxy":                       {Text: "Proxy", Machine: true},
			"Only in the source language": {Text: "Nur in der Quelle", Reviewed: true},
			"Saved {n} file":              {Other: "{n} Dateien gespeichert"},
		},
	}
	if _, err := Install("de", testLedger(), []Catalog{testEn(), testZh(), de}, ""); err != nil {
		t.Fatal(err)
	}
	var info Info
	for _, i := range Available() {
		if i.Language == "de" {
			info = i
		}
	}
	if info.Translated != 3 || info.Machine != 1 || info.Reviewed != 1 {
		t.Errorf("machine/reviewed counts are wrong: %+v", info)
	}
	if got := Missing("de"); len(got) != 1 || got[0] != "An external file {names} is still in {dir}" {
		t.Errorf("a missing message reported an error: %v", got)
	}
}

func TestWidthComesFromTheCatalog(t *testing.T) {
	install(t, "zh-Hans")
	if got := Width("label", 99); got != 8 {
		t.Errorf("the Chinese label width must be 8, got %d", got)
	}
	install(t, "en")
	if got := Width("label", 99); got != 10 {
		t.Errorf("the English label width must be 10, got %d", got)
	}
	if got := Width("nope", 99); got != 99 {
		t.Errorf("an uncalibrated size must use the default, got %d", got)
	}
}

func TestDiskOverlayOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	overlay := `{"meta":{"language":"zh-Hans"},"messages":{
		"Proxy":"代理（本地改的）",
		"Never listed anywhere":"凭空多出来的"}}`
	if err := os.WriteFile(OverlayPath(dir, "zh-Hans"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("zh-Hans", testLedger(), []Catalog{testEn(), testZh()}, dir); err != nil {
		t.Fatal(err)
	}
	if got := T("Proxy", nil); got != "代理（本地改的）" {
		t.Errorf("the disk overlay did not take effect: %q", got)
	}
	if got := Overlaid(); got != 1 {
		t.Errorf("the overlay count must be 1 (messages not in the ledger do not count), got %d", got)
	}
	// The overlay cannot change a message that is not in the ledger -- it does not take effect,
	// and it must not break anything else either
	if got := T("Saved {n} file", A{"n": 2}); got != "已保存 2 个文件" {
		t.Errorf("another message was broken by the overlay: %q", got)
	}
}

func TestBrokenOverlayIsIgnoredLoudly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(OverlayPath(dir, "zh-Hans"), []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("zh-Hans", testLedger(), []Catalog{testEn(), testZh()}, dir); err != nil {
		t.Fatalf("a broken overlay file must not block startup: %v", err)
	}
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("after a broken overlay the builtin table must still work: %q", got)
	}
}

func TestErrorCarriesIdentityNotProse(t *testing.T) {
	install(t, "zh-Hans")
	base := errors.New("permission denied")
	err := Ef(base, "Cannot read mappings: {err}", A{"path": "/x"})
	if got := ID(err); got != "Cannot read mappings: {err}" {
		t.Errorf("the error identity must be the source-language text, got %q", got)
	}
	if got := err.Error(); got != "Cannot read mappings: permission denied" {
		t.Errorf("what renders must be the translation (here the English fallback): %q", got)
	}
	if !errors.Is(err, base) {
		t.Error("the %w semantics must be kept: errors.Is must see through")
	}
	// An error that is not *Error is passed through as is (someone else's error, do not translate
	// it)
	if got := ID(base); got != "permission denied" {
		t.Errorf("someone else's error must be returned as is: %q", got)
	}
	if ID(nil) != "" {
		t.Error("a nil error must give an empty string")
	}
	// Rendered in Chinese (translate this sentence)
	if _, err := Install("zh-Hans", testLedger(), []Catalog{testEn(), Catalog{
		Language: "zh-Hans",
		Messages: map[string]Entry{"Cannot read mappings: {err}": {Text: "读不了配置映射：{err}"}},
	}}, ""); err != nil {
		t.Fatal(err)
	}
	err2 := Ef(base, "Cannot read mappings: {err}", A{"path": "/x"})
	if got := err2.Error(); got != "读不了配置映射：permission denied" {
		t.Errorf("the error must render in the current language: %q", got)
	}
	if got := ID(err2); got != "Cannot read mappings: {err}" {
		t.Errorf("the identity must not change with the language: %q", got)
	}
}

// TestExtendAddsNewMessagesOnly locks down the path where "a distribution brings its own copy".
//
// It locks two things at once: (a) an appended **new** id takes effect immediately; (b) an id
// the kernel has already stated cannot be changed by an append -- the wording belongs to the
// layer that speaks it, and changing the kernel's wording goes through the kernel or the disk
// overlay, not by a late-arriving module quietly overriding it.
func TestExtendAddsNewMessagesOnly(t *testing.T) {
	install(t, "zh-Hans")
	if got := T("Proxy", nil); got != "代理" {
		t.Fatalf("after installing it must be Chinese: %q", got)
	}

	const own = "hello from the distribution"
	err := Extend(Ledger{Messages: map[string]LedgerEntry{
		own: {},
	}}, []Catalog{{Language: "zh-Hans", Messages: map[string]Entry{
		own:     {Text: "发行版自己的一句话"},
		"Proxy": {Text: "内核已经说过的，不该被改掉"},
	}}})
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}

	if got := T(own, nil); got != "发行版自己的一句话" {
		t.Errorf("the appended translation did not take effect: %q", got)
	}
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("the append overrode a message the kernel already had: %q", got)
	}
	// Coverage must count the appended ones: otherwise half of the distribution's UI does not
	// exist on `newgate lang`.
	var zh Info
	for _, in := range Available() {
		if in.Language == "zh-Hans" {
			zh = in
		}
	}
	if zh.Total != len(testLedger().Messages)+1 {
		t.Errorf("the ledger has %d entries after the append, want %d", zh.Total, len(testLedger().Messages)+1)
	}
	for _, id := range Missing("zh-Hans") {
		if id == own {
			t.Error("this appended entry is already translated and must not count as missing")
		}
	}

	// Under the source language an append does not change the identity path: that English sentence
	// comes out as is, not one byte moved.
	install(t, SourceLang)
	if err := Extend(Ledger{}, []Catalog{{Language: SourceLang, Messages: map[string]Entry{
		own: {Text: "不该在这条路径上出现"},
	}}}); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if got := T(own, nil); got != own {
		t.Errorf("the source-language path was changed by the append: %q", got)
	}

	// A catalog without meta.language is a broken catalog: silently dropping it means the
	// distribution's UI always speaks English.
	if err := Extend(Ledger{}, []Catalog{{Messages: map[string]Entry{own: {Text: "x"}}}}); err == nil {
		t.Error("a catalog without meta.language must error")
	}
}

func TestCatalogRoundTripKeepsEverything(t *testing.T) {
	in := Catalog{
		Language: "zh-Hans",
		Source:   SourceLang,
		Widths:   map[string]int{"label": 8},
		Messages: map[string]Entry{
			"plain":      {Text: "纯正文"},
			"plural {n}": {Other: "{n} 个"},
			"machine":    {Text: "机翻的", Machine: true},
			"noted":      {Text: "有备注", Note: "给译者看"},
			"reviewed":   {Text: "复核过的", Reviewed: true},
		},
	}
	raw, err := MarshalCatalog(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseCatalog(raw)
	if err != nil {
		t.Fatalf("cannot decode the translations it generated itself: %v\n%s", err, raw)
	}
	if out.Language != in.Language || out.Source != in.Source || out.Widths["label"] != 8 {
		t.Errorf("meta was lost: %+v", out)
	}
	for id, want := range in.Messages {
		if got := out.Messages[id]; got != want {
			t.Errorf("%s changed after the round trip: %+v vs %+v", id, got, want)
		}
	}
	// A message with only a body must be written as a bare string for the file to be readable
	if !strings.Contains(string(raw), `"plain": "纯正文"`) {
		t.Errorf("a plain-body message must be written as a bare string:\n%s", raw)
	}
	// Keys sorted: random map iteration order makes every generated diff wobble
	if strings.Index(string(raw), `"machine"`) > strings.Index(string(raw), `"noted"`) {
		t.Errorf("keys are not sorted:\n%s", raw)
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	in := testLedger()
	raw, err := MarshalLedger(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseLedger(raw)
	if err != nil {
		t.Fatalf("cannot decode the ledger it generated itself: %v\n%s", err, raw)
	}
	if len(out.Messages) != len(in.Messages) {
		t.Fatalf("wrong entry count: %d vs %d", len(out.Messages), len(in.Messages))
	}
	for id, want := range in.Messages {
		got, ok := out.Messages[id]
		if !ok {
			t.Errorf("the ledger lost %q", id)
			continue
		}
		if got.Other != want.Other ||
			strings.Join(got.Args, ",") != strings.Join(want.Args, ",") {
			t.Errorf("%s changed after the round trip: %+v vs %+v", id, got, want)
		}
	}
	// The fact "plural" must be readable from the ledger (coverage and translation tools both need
	// it)
	if !out.Messages["Saved {n} file"].Plural() {
		t.Error("the ledger must remember which message has a plural")
	}
	if out.Messages["Proxy"].Plural() {
		t.Error("a message without a plural must not be marked as plural")
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	// A misspelled field name must error loudly: the symptom of silently ignoring it is "I marked
	// it as machine translated, why isn't it marked"
	_, err := ParseCatalog([]byte(`{"meta":{"language":"zh-Hans"},"messages":{"x":{"text":"y","machien":true}}}`))
	if err == nil {
		t.Fatal("a misspelled field name must error")
	}
	if _, err := ParseCatalog([]byte(`{"meta":{"language":"zh-Hans"},"messages":{}}`)); err != nil {
		t.Fatalf("an empty table is valid: %v", err)
	}
	if _, err := ParseCatalog([]byte(`{"meta":{},"messages":{}}`)); err == nil {
		t.Fatal("a catalog without meta.language must error")
	}
	if _, err := ParseLedger([]byte(`{"messages":{"x":{"args":["dir"]}}}`)); err != nil {
		t.Fatalf("the ledger must decode: %v", err)
	}
	// The `where` cell was deleted on 2026-09-21 (reasoning in the Ledger comment). If it still
	// appears in an old ledger, DisallowUnknownFields errors on the spot -- and that is exactly
	// what we want: an old ledger must be explicitly regenerated, not have a whole field silently
	// ignored.
	if _, err := ParseLedger([]byte(`{"messages":{"x":{"where":"a.go:1"}}}`)); err == nil {
		t.Fatal("a ledger that still has where must error (that cell was deleted)")
	}
	if _, err := ParseLedger([]byte(`{"massages":{}}`)); err == nil {
		t.Fatal("a misspelled ledger field name must error")
	}
}

func TestBuiltinCatalogsAreWellFormed(t *testing.T) {
	led, cats, err := Builtin()
	if err != nil {
		t.Fatalf("cannot read the builtin catalogs: %v", err)
	}
	if len(cats) < 1 {
		t.Fatalf("there must be at least one builtin translation besides the source language, got %d", len(cats))
	}
	for _, c := range cats {
		if c.Language == SourceLang {
			t.Errorf("%s must not have a builtin translation file -- the source-language text lives in the source code", SourceLang)
		}
		if len(c.Widths) == 0 {
			t.Errorf("%s does not declare layout widths (meta.widths) -- column widths will skew when the language changes", c.Language)
		}
	}
	// The ledger may be empty for now (no copy migrated yet), but it must still parse
	_ = led
}

func TestPlaceholderExtraction(t *testing.T) {
	got := Placeholders("a {b} and {c} and {b}")
	if strings.Join(got, ",") != "b,c" {
		t.Errorf("placeholder extraction is wrong: %v", got)
	}
	if got := Placeholders("没有占位符 { 半个"); strings.Join(got, ",") != "" {
		t.Errorf("half a brace must not be treated as a placeholder: %v", got)
	}
	union := EntryPlaceholders(Entry{One: "{n} thing", Other: "{n} things in {dir}"})
	if strings.Join(union, ",") != "dir,n" {
		t.Errorf("the singular/plural union is wrong: %v", union)
	}
	// Copy contains **machine markers**, and JSON carries braces itself. Those braces are bytes,
	// not slots: treated as slots, `tools/i18n check` would point at a perfectly correct sentence
	// and report `type":` `"disabled` as a missing argument, while the runtime format would try to
	// substitute it. Identifier shape separates the two.
	const withJSON = `injected thinking:{"type":"disabled"} ({why})`
	if got := Placeholders(withJSON); strings.Join(got, ",") != "why" {
		t.Errorf("embedded JSON must not be treated as a placeholder: %v", got)
	}
	if got := format(withJSON, A{"why": "关掉思考"}); got != `injected thinking:{"type":"disabled"} (关掉思考)` {
		t.Errorf("the embedded JSON was modified: %q", got)
	}
}

func TestCatalogsFromFSRejectsMismatchedFilename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zh-Hans.json"),
		[]byte(`{"meta":{"language":"zh-Hant"},"messages":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CatalogsFromFS(os.DirFS(dir), "."); err == nil {
		t.Fatal("a filename that disagrees with meta.language must error")
	}
}

// TestUseSwitchesLanguageWithoutReinstalling locks down the "switch language at runtime" path.
//
// This is the entire reason the language card on the web UI can exist: the daemon is
// long-lived, and the user must be able to switch **on the spot** after picking a language,
// without waiting for a restart. And there is one thing very easy to get wrong while switching
// -- taking the easy route and calling Install again rebuilds builtins entirely, so every
// translation the distribution Extend-ed in at assembly time disappears. The symptom is "after
// switching to Chinese on the web UI, half of the distribution's UI went back to English",
// which looks like "a few are untranslated".
//
// So the assertion is on **whether the appended entry is still there after switching away and
// back** -- with only one switch, the wiped catalog shows no difference at that moment (the
// current language is still using the kernel's copy).
func TestUseSwitchesLanguageWithoutReinstalling(t *testing.T) {
	install(t, "zh-Hans")

	const own = "a sentence only the distribution has"
	if err := Extend(Ledger{Messages: map[string]LedgerEntry{own: {}}},
		[]Catalog{{Language: "zh-Hans", Messages: map[string]Entry{own: {Text: "发行版自己的一句话"}}}}); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if got := T(own, nil); got != "发行版自己的一句话" {
		t.Fatalf("the appended translation must take effect: %q", got)
	}

	// Switch to the source language: the sentence comes out as is (identity path).
	if got := Use(SourceLang); got != SourceLang {
		t.Fatalf("Use(%q) = %q", SourceLang, got)
	}
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("after switching to the source language it must come out as is: %q", got)
	}

	// Switch back: **the appended entry must still be there**. That is this test's real assertion.
	if got := Use("zh-Hans"); got != "zh-Hans" {
		t.Fatalf("Use(zh-Hans) = %q", got)
	}
	if got := T(own, nil); got != "发行版自己的一句话" {
		t.Errorf("after switching back the distribution's translation is gone -- this switch reinstalled the catalog tables: %q", got)
	}
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("the kernel's sentence must come back too: %q", got)
	}
	if got := Current(); got != "zh-Hans" {
		t.Errorf("Current() must follow along: %q", got)
	}
}

// TestUseFallsBackToTheSourceLanguage: switching to a language that does not exist is not an
// error, it is a fallback.
//
// Same rule as the Install one: the values on the UI come from Available(), so this is not
// normally reached, but a gate must not hang the whole process because "the caller should not
// call it that way" -- language is a preference, not part of configuration correctness.
func TestUseFallsBackToTheSourceLanguage(t *testing.T) {
	install(t, "zh-Hans")
	if got := Use("fr-FR"); got != SourceLang {
		t.Errorf("an untranslated language must fall back to the source language, got %q", got)
	}
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("after the fallback it must take the identity path: %q", got)
	}
}
