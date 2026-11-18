package i18n

import (
	"encoding/binary"
	"io/fs"
	"os"
	"slices"
	"testing"
)

// catalogsJSONForTest lets the tests read the JSON **on disk**: it is no longer in the embed
// (what is embedded is the bundle), and these tests are exactly verifying "the bundle agrees
// with the JSON".
//
// It lives in the test file rather than catalogs.go: at runtime there must be no path that goes
// through JSON, that is exactly the cost this pass removes -- put a "tests only" entrance in
// shipped code and the next person will think it is a real API.
func catalogsJSONForTest() fs.FS { return os.DirFS(".") }

// The compile-time form of the catalog tables (see bundle.go). These three tests lock the three
// links of one chain: the encoded bytes read back, **what reads back must be field-for-field
// identical to the JSON**, and the copy embedded in the binary really is the new one (not
// "edited the JSON, forgot to regenerate").
//
// Why it is worth it: this is a **derived artifact**, and derived artifacts fail silently --
// when the `.bin` lags behind the `.json`, the UI still runs, it is just missing the few
// translations added recently, and nobody reports an error. So "reads back" does not count; it
// has to match the JSON.

func sampleCatalog() Catalog {
	return Catalog{
		Language: "zh-Hans", Source: "en",
		Widths: map[string]int{"label": 8, "term": 12},
		Messages: map[string]Entry{
			"plain":      {Text: "普通"},
			"plural {n}": {One: "一个", Other: "{n} 个", Reviewed: true},
			"noted":      {Text: "带备注", Note: "给译者看", Machine: true},
			"空":          {},
		},
	}
}

// TestABundleRoundTrips: encode then read back, must be field-for-field identical.
func TestABundleRoundTrips(t *testing.T) {
	want := sampleCatalog()
	got, err := DecodeCatalog(EncodeCatalog(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != want.Language || got.Source != want.Source {
		t.Errorf("language/source do not match: %+v", got)
	}
	if len(got.Widths) != len(want.Widths) || got.Widths["label"] != 8 {
		t.Errorf("layout widths do not match: %+v", got.Widths)
	}
	if len(got.Messages) != len(want.Messages) {
		t.Fatalf("entry counts do not match: %d vs %d", len(got.Messages), len(want.Messages))
	}
	for id, w := range want.Messages {
		g, ok := got.Messages[id]
		if !ok {
			t.Errorf("one entry is missing: %q", id)
			continue
		}
		if g != w && !(g.Empty() && w.Empty()) {
			t.Errorf("%q does not match:\n  want %+v\n  got %+v", id, w, g)
		}
	}
	// Plurals and flags are the two things **easily lost in a binary** (they are not text, they
	// are shape).
	if e := got.Messages["plural {n}"]; !e.plural() || !e.Reviewed {
		t.Errorf("the plural/reviewed flags were lost: %+v", e)
	}
	if e := got.Messages["noted"]; !e.Machine || e.Note != "给译者看" {
		t.Errorf("the machine flag/note was lost: %+v", e)
	}
}

// TestABundleIsDeterministic: the same input encoded twice must be byte-for-byte identical.
//
// This is the premise that makes `-check` work: if it were not deterministic, it would report
// "stale" every time, and a false alarm like that makes people turn the check off -- and then
// the real error it was meant to catch is never caught again.
func TestABundleIsDeterministic(t *testing.T) {
	a := EncodeCatalog(sampleCatalog())
	b := EncodeCatalog(sampleCatalog())
	if string(a) != string(b) {
		t.Error("the same catalog encodes to different bytes (did map iteration order leak into the output?)")
	}
}

// TestABundleRefusesGarbage: bad input must error, **not crash**.
//
// The catalog tables are read in Start, and one panic keeps the whole process from starting
// (not even `newgate status` runs, and that command is exactly what you use to investigate "why
// it does not start"). So every out-of-bounds case becomes an error.
func TestABundleRefusesGarbage(t *testing.T) {
	good := EncodeCatalog(sampleCatalog())
	cases := map[string][]byte{
		"not a bundle":                  []byte("hello"),
		"magic matches, body truncated": good[:20],
		"the last 10 bytes chopped off": good[:len(good)-10],
		"the length field is garbage":   append(append([]byte(nil), bundleMagic...), 'c', 0xff, 0xff, 0xff, 0xff),
	}
	for name, raw := range cases {
		if _, err := DecodeCatalog(raw); err == nil {
			t.Errorf("%s: expected an error, but it read successfully", name)
		}
	}
	// A type mismatch must error too (reading a ledger as a catalog).
	if _, err := DecodeCatalog(EncodeLedger(Ledger{Messages: map[string]LedgerEntry{"x": {Args: []string{"y"}}}})); err == nil {
		t.Error("the ledger was read successfully as a catalog; it must error")
	}
}

// TestAHostileCountDoesNotAllocate: the count field is **a number written in the file** and
// must not be handed straight to `make(map, n)`.
//
// What this guards against is not "reading wrong data" but "crashing while reading":
// `make(map[K]V, 4 billion)` demands a huge allocation **before the first bounds check takes
// effect** -- the symptom is an OOM at startup rather than "this bundle is broken". And the
// catalog tables are read in Start, so failing to start means `newgate status` does not run
// either, and that command is exactly what you use to investigate "why it does not start".
//
// The assertion is "does not panic and reports an error": allocating that size would crash the
// test machine outright, so it going red means the check really works.
func TestAHostileCountDoesNotAllocate(t *testing.T) {
	mk := func(kind byte, count uint32) []byte {
		raw := append([]byte(nil), bundleMagic...)
		raw = append(raw, kind)
		if kind == bundleKindCatalog {
			raw = append(raw, 0, 0, 0, 0) // language: empty string
			raw = append(raw, 0, 0, 0, 0) // source: empty string
			raw = append(raw, 0, 0, 0, 0) // widths: zero entries
		}
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], count)
		return append(raw, tmp[:]...)
	}
	huge := uint32(0xFFFFFFF0)
	if _, err := DecodeCatalog(mk(bundleKindCatalog, huge)); err == nil {
		t.Error("an astronomical entry count must error")
	}
	// Same for the ledger: its count field is earlier, and goes through a different make.
	if _, err := DecodeLedger(mk(bundleKindLedger, huge)); err == nil {
		t.Error("an astronomical ledger entry count must error")
	}
}

// TestTheEmbeddedBundlesMatchTheJSON: the copy embedded in the binary must be the same as the
// JSON in the repository.
//
// This is the ratchet for "edited the JSON but forgot to run bundle". The check is on
// **content**: decode the embedded bundle, decode the JSON on disk, and compare the two field
// by field.
func TestTheEmbeddedBundlesMatchTheJSON(t *testing.T) {
	led, cats, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(led.Messages) == 0 || len(cats) == 0 {
		t.Fatal("the embedded catalog tables are empty")
	}
	fromJSON, err := ledgerFromFS(catalogsJSONForTest(), "catalogs")
	if err != nil {
		t.Fatal(err)
	}
	if len(fromJSON.Messages) != len(led.Messages) {
		t.Fatalf("ledger entries: bundle %d, JSON %d -- edited the JSON without running `tools/i18n bundle`?",
			len(led.Messages), len(fromJSON.Messages))
	}
	for id, want := range fromJSON.Messages {
		got, ok := led.Messages[id]
		if !ok {
			t.Errorf("%q is missing from the ledger (bundle behind the JSON?)", id)
			continue
		}
		if got.One != want.One ||
			got.Other != want.Other || got.Note != want.Note ||
			!slices.Equal(got.Args, want.Args) {
			t.Errorf("ledger entry %q does not match (bundle behind the JSON?)\n  want %+v\n  got %+v", id, want, got)
		}
	}
	jsonCats, err := CatalogsFromFS(catalogsJSONForTest(), "catalogs")
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonCats) != len(cats) {
		t.Fatalf("language counts: bundle %d, JSON %d", len(cats), len(jsonCats))
	}
	for i := range jsonCats {
		want, got := jsonCats[i], cats[i]
		if want.Language != got.Language || len(want.Messages) != len(got.Messages) {
			t.Fatalf("%s does not match (bundle behind the JSON?)", want.Language)
		}
		for id, we := range want.Messages {
			ge, ok := got.Messages[id]
			if !ok || ge != we {
				t.Errorf("%s / %q does not match (bundle behind the JSON?)\n  want %+v\n  got %+v", want.Language, id, we, ge)
			}
		}
	}
}
