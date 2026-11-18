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
			"Saved {n} file": {Other: "已保存 {n} 个文件"}, // 中文不分单复数
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
			t.Errorf("Normalize(%q) = %q，期望 %q", in, got, want)
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
		"zh":          "zh-Hans", // 裸 zh 按简体
		"zh-Hant-HK":  "zh-Hant",
		"en-US":       "en",
		"en":          "en",
		"fr-FR":       "", // 没翻法语 → 交给上层回退到源语言
		"C":           "",
		"":            "",
	}
	for in, want := range cases {
		if got := Match(in, avail); got != want {
			t.Errorf("Match(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestSourceLanguageIsIdentity(t *testing.T) {
	install(t, "en")
	// Source language path: the sentence in the code is the final text, not a character of catalog
	// data is used
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("源语言该恒等: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 3, nil); got != "Saved 3 files" {
		t.Errorf("源语言复数: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 1, nil); got != "Saved 1 file" {
		t.Errorf("源语言单数: %q", got)
	}
	// A sentence that is not in the catalog at all still comes out as is -- there is no such thing
	// as a "key", so there is no misspelled key
	if got := T("Never listed anywhere", nil); got != "Never listed anywhere" {
		t.Errorf("未登记的消息该原样输出: %q", got)
	}
}

func TestTranslationAndFallback(t *testing.T) {
	install(t, "zh-Hans")
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("中文表没生效: %q", got)
	}
	// Missing translation -> falls back to the English sentence in the source (not the key name,
	// because the key is that sentence)
	if got := T("Only in the source language", nil); got != "Only in the source language" {
		t.Errorf("缺译文时该回退源语言: %q", got)
	}
}

func TestPlaceholders(t *testing.T) {
	install(t, "zh-Hans")
	got := T("An external file {names} is still in {dir}", A{"dir": "/usr/local/bin", "names": "claude"})
	want := "同名外部文件仍在 /usr/local/bin：claude" // 语序与英文不同，具名占位符才做得到
	if got != want {
		t.Errorf("具名占位符替换错了:\n got %q\nwant %q", got, want)
	}
	// One argument too few: that placeholder is **left as is** (pinpointing what is missing here),
	// not silently emptied
	got = T("An external file {names} is still in {dir}", A{"dir": "/x"})
	if !strings.Contains(got, "{names}") {
		t.Errorf("缺实参时该留下占位符，实际 %q", got)
	}
}

func TestPlural(t *testing.T) {
	install(t, "zh-Hans")
	if got := N("Saved {n} file", "Saved {n} files", 1, nil); got != "已保存 1 个文件" {
		t.Errorf("中文不分单复数: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 5, nil); got != "已保存 5 个文件" {
		t.Errorf("中文不分单复数: %q", got)
	}
	// The n given at the call site and the {n} in the translation are the same one (withN fills it
	// in)
	if got := N("Saved {n} file", "Saved {n} files", 1, A{"n": 7}); got != "已保存 7 个文件" {
		t.Errorf("显式给的 n 该优先: %q", got)
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
		t.Errorf("英文单数: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 0, nil); got != "Saved 0 files" {
		t.Errorf("英文 0 用复数: %q", got)
	}
	if got := N("Saved {n} file", "Saved {n} files", 2, nil); got != "Saved 2 files" {
		t.Errorf("英文复数: %q", got)
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
		t.Fatalf("该回退到源语言，实际 %q", eff)
	}
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("回退后该是英文: %q", got)
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
		t.Errorf("账本里共 4 条，实际报 %d", zhInfo.Total)
	}
	if zhInfo.Translated != 3 {
		t.Errorf("中文翻了 3 条，实际报 %d", zhInfo.Translated)
	}
	miss := Missing("zh-Hans")
	if len(miss) != 1 || miss[0] != "Only in the source language" {
		t.Errorf("缺的消息报错了: %v", miss)
	}
	if len(Missing(SourceLang)) != 0 {
		t.Error("源语言不该缺任何消息")
	}
	// The source language is always in the available list (it needs no catalog file)
	var sawSource bool
	for _, i := range Available() {
		if i.Language == SourceLang {
			sawSource = true
		}
	}
	if !sawSource {
		t.Error("源语言必须在可选列表里")
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
		t.Errorf("机翻/已复核统计不对: %+v", info)
	}
	if got := Missing("de"); len(got) != 1 || got[0] != "An external file {names} is still in {dir}" {
		t.Errorf("缺的消息报错了: %v", got)
	}
}

func TestWidthComesFromTheCatalog(t *testing.T) {
	install(t, "zh-Hans")
	if got := Width("label", 99); got != 8 {
		t.Errorf("中文 label 宽度该是 8，实际 %d", got)
	}
	install(t, "en")
	if got := Width("label", 99); got != 10 {
		t.Errorf("英文 label 宽度该是 10，实际 %d", got)
	}
	if got := Width("nope", 99); got != 99 {
		t.Errorf("没标定的尺寸该用缺省值，实际 %d", got)
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
		t.Errorf("磁盘覆盖没生效: %q", got)
	}
	if got := Overlaid(); got != 1 {
		t.Errorf("覆盖计数该是 1（账本里没有的消息不算），实际 %d", got)
	}
	// The overlay cannot change a message that is not in the ledger -- it does not take effect,
	// and it must not break anything else either
	if got := T("Saved {n} file", A{"n": 2}); got != "已保存 2 个文件" {
		t.Errorf("别的消息被覆盖搞坏了: %q", got)
	}
}

func TestBrokenOverlayIsIgnoredLoudly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(OverlayPath(dir, "zh-Hans"), []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("zh-Hans", testLedger(), []Catalog{testEn(), testZh()}, dir); err != nil {
		t.Fatalf("坏掉的覆盖文件不该挡住启动: %v", err)
	}
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("坏覆盖之后内置表该照常工作: %q", got)
	}
}

func TestErrorCarriesIdentityNotProse(t *testing.T) {
	install(t, "zh-Hans")
	base := errors.New("permission denied")
	err := Ef(base, "Cannot read mappings: {err}", A{"path": "/x"})
	if got := ID(err); got != "Cannot read mappings: {err}" {
		t.Errorf("错误身份该是源语言原文，实际 %q", got)
	}
	if got := err.Error(); got != "Cannot read mappings: permission denied" {
		t.Errorf("渲染出来的该是译文（这里是回退的英文）: %q", got)
	}
	if !errors.Is(err, base) {
		t.Error("%w 语义该保留：errors.Is 要能穿透")
	}
	// An error that is not *Error is passed through as is (someone else's error, do not translate
	// it)
	if got := ID(base); got != "permission denied" {
		t.Errorf("别人的错误该原样返回: %q", got)
	}
	if ID(nil) != "" {
		t.Error("nil 错误该给空串")
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
		t.Errorf("错误该按当前语言渲染: %q", got)
	}
	if got := ID(err2); got != "Cannot read mappings: {err}" {
		t.Errorf("身份不该随语言变: %q", got)
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
		t.Fatalf("装完就该是中文: %q", got)
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
		t.Errorf("追加的译文没生效: %q", got)
	}
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("追加盖掉了内核已有的消息: %q", got)
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
		t.Errorf("追加后账本 = %d 条，想要 %d 条", zh.Total, len(testLedger().Messages)+1)
	}
	for _, id := range Missing("zh-Hans") {
		if id == own {
			t.Error("追加的这条已经翻了，不该还算缺")
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
		t.Errorf("源语言路径被追加改变了: %q", got)
	}

	// A catalog without meta.language is a broken catalog: silently dropping it means the
	// distribution's UI always speaks English.
	if err := Extend(Ledger{}, []Catalog{{Messages: map[string]Entry{own: {Text: "x"}}}}); err == nil {
		t.Error("没写 meta.language 的目录该报错")
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
		t.Fatalf("自己生成的译文解不回来: %v\n%s", err, raw)
	}
	if out.Language != in.Language || out.Source != in.Source || out.Widths["label"] != 8 {
		t.Errorf("meta 丢了: %+v", out)
	}
	for id, want := range in.Messages {
		if got := out.Messages[id]; got != want {
			t.Errorf("%s 往返后变了: %+v vs %+v", id, got, want)
		}
	}
	// A message with only a body must be written as a bare string for the file to be readable
	if !strings.Contains(string(raw), `"plain": "纯正文"`) {
		t.Errorf("纯正文消息该写成裸字符串:\n%s", raw)
	}
	// Keys sorted: random map iteration order makes every generated diff wobble
	if strings.Index(string(raw), `"machine"`) > strings.Index(string(raw), `"noted"`) {
		t.Errorf("键没排序:\n%s", raw)
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
		t.Fatalf("自己生成的账本解不回来: %v\n%s", err, raw)
	}
	if len(out.Messages) != len(in.Messages) {
		t.Fatalf("条数不对: %d vs %d", len(out.Messages), len(in.Messages))
	}
	for id, want := range in.Messages {
		got, ok := out.Messages[id]
		if !ok {
			t.Errorf("账本丢了 %q", id)
			continue
		}
		if got.Other != want.Other ||
			strings.Join(got.Args, ",") != strings.Join(want.Args, ",") {
			t.Errorf("%s 往返后变了: %+v vs %+v", id, got, want)
		}
	}
	// The fact "plural" must be readable from the ledger (coverage and translation tools both need
	// it)
	if !out.Messages["Saved {n} file"].Plural() {
		t.Error("账本该记住哪条消息带复数")
	}
	if out.Messages["Proxy"].Plural() {
		t.Error("不带复数的消息不该被标成复数")
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	// A misspelled field name must error loudly: the symptom of silently ignoring it is "I marked
	// it as machine translated, why isn't it marked"
	_, err := ParseCatalog([]byte(`{"meta":{"language":"zh-Hans"},"messages":{"x":{"text":"y","machien":true}}}`))
	if err == nil {
		t.Fatal("拼错的字段名该报错")
	}
	if _, err := ParseCatalog([]byte(`{"meta":{"language":"zh-Hans"},"messages":{}}`)); err != nil {
		t.Fatalf("空表是合法的: %v", err)
	}
	if _, err := ParseCatalog([]byte(`{"meta":{},"messages":{}}`)); err == nil {
		t.Fatal("没有 meta.language 该报错")
	}
	if _, err := ParseLedger([]byte(`{"messages":{"x":{"args":["dir"]}}}`)); err != nil {
		t.Fatalf("账本该解得出来: %v", err)
	}
	// The `where` cell was deleted on 2026-09-21 (reasoning in the Ledger comment). If it still
	// appears in an old ledger, DisallowUnknownFields errors on the spot -- and that is exactly
	// what we want: an old ledger must be explicitly regenerated, not have a whole field silently
	// ignored.
	if _, err := ParseLedger([]byte(`{"messages":{"x":{"where":"a.go:1"}}}`)); err == nil {
		t.Fatal("账本里还留着 where 该报错（那一格已经删了）")
	}
	if _, err := ParseLedger([]byte(`{"massages":{}}`)); err == nil {
		t.Fatal("拼错的账本字段名该报错")
	}
}

func TestBuiltinCatalogsAreWellFormed(t *testing.T) {
	led, cats, err := Builtin()
	if err != nil {
		t.Fatalf("内置目录读不出来: %v", err)
	}
	if len(cats) < 1 {
		t.Fatalf("内置至少要有源语言以外的一份译文，实际 %d 份", len(cats))
	}
	for _, c := range cats {
		if c.Language == SourceLang {
			t.Errorf("%s 不该有内置译文文件——源语言的文本就在源码里", SourceLang)
		}
		if len(c.Widths) == 0 {
			t.Errorf("%s 没标版式尺寸（meta.widths）——换语言时列宽会歪", c.Language)
		}
	}
	// The ledger may be empty for now (no copy migrated yet), but it must still parse
	_ = led
}

func TestPlaceholderExtraction(t *testing.T) {
	got := Placeholders("a {b} and {c} and {b}")
	if strings.Join(got, ",") != "b,c" {
		t.Errorf("占位符提取错了: %v", got)
	}
	if got := Placeholders("没有占位符 { 半个"); strings.Join(got, ",") != "" {
		t.Errorf("半个花括号不该被当成占位符: %v", got)
	}
	union := EntryPlaceholders(Entry{One: "{n} thing", Other: "{n} things in {dir}"})
	if strings.Join(union, ",") != "dir,n" {
		t.Errorf("单复数并集错了: %v", union)
	}
	// Copy contains **machine markers**, and JSON carries braces itself. Those braces are bytes,
	// not slots: treated as slots, `tools/i18n check` would point at a perfectly correct sentence
	// and report `type":` `"disabled` as a missing argument, while the runtime format would try to
	// substitute it. Identifier shape separates the two.
	const withJSON = `injected thinking:{"type":"disabled"} ({why})`
	if got := Placeholders(withJSON); strings.Join(got, ",") != "why" {
		t.Errorf("内嵌的 JSON 不该被当成占位符: %v", got)
	}
	if got := format(withJSON, A{"why": "关掉思考"}); got != `injected thinking:{"type":"disabled"} (关掉思考)` {
		t.Errorf("内嵌的 JSON 被改动了: %q", got)
	}
}

func TestCatalogsFromFSRejectsMismatchedFilename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zh-Hans.json"),
		[]byte(`{"meta":{"language":"zh-Hant"},"messages":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CatalogsFromFS(os.DirFS(dir), "."); err == nil {
		t.Fatal("文件名与 meta.language 不一致时该报错")
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
		t.Fatalf("追加的译文该生效: %q", got)
	}

	// Switch to the source language: the sentence comes out as is (identity path).
	if got := Use(SourceLang); got != SourceLang {
		t.Fatalf("Use(%q) = %q", SourceLang, got)
	}
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("换成源语言后该原样输出: %q", got)
	}

	// Switch back: **the appended entry must still be there**. That is this test's real assertion.
	if got := Use("zh-Hans"); got != "zh-Hans" {
		t.Fatalf("Use(zh-Hans) = %q", got)
	}
	if got := T(own, nil); got != "发行版自己的一句话" {
		t.Errorf("换回来之后发行版的译文没了——说明这次换语言重装了目录表: %q", got)
	}
	if got := T("Proxy", nil); got != "代理" {
		t.Errorf("内核那句也该回来: %q", got)
	}
	if got := Current(); got != "zh-Hans" {
		t.Errorf("Current() 该跟着变: %q", got)
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
		t.Errorf("没翻的语言该回退到源语言，实际 %q", got)
	}
	if got := T("Proxy", nil); got != "Proxy" {
		t.Errorf("回退之后该走恒等路径: %q", got)
	}
}
