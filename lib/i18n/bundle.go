package i18n

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// The **compile-time form** of the catalog tables: parsing is done at generation time, and at
// runtime they are only read by format.
//
// # Why it deserves a format
//
// Measured on 2026-09-21: assembling each `newgate …` command takes about 6ms, of which **88%
// is re-parsing the 250KB catalog JSON on every process** (`Builtin()` alone measures 2.78ms,
// while `Install` building the lookup table takes only 90µs). What really should move to
// compile time is **this**, not resolve -- component.Resolve's validation plus topological sort
// is 31µs (0.5%).
//
// So: `tools/i18n bundle` compiles catalogs/*.json into this format at generation time (checked
// into version control, `-check` blocks staleness),
// and at runtime `Builtin()` reads it directly. **JSON is still the only truth** -- people edit
// JSON, tools read and write JSON, and this format is a pure derivative that can be deleted and
// regenerated at any time.
//
// # Why not "just decode into a map and be done"
//
// After decoding, the format is still `Catalog`/ `Ledger` (with maps inside), and **not a word
// of the public API changed**: `T()` on the gateway log hot path is still a hash lookup. Only
// the "bytes to map" step was swapped -- from "JSON lexing + 2800 reflection deserializations"
// to "read a length prefix + insert into a map".
//
// # Format
//
//	magic "NGCAT2\n" (8 bytes)
//	(NGCAT1 is the format from when there was a Where cell. Changing the format requires changing the
//	magic: decoding an old .bin with the new layout reads garbage all the way without an error --
//	that is the classic bad way for "the interpreter does not match the grammar".)
//	type         1 byte   'c' catalog / 'l' ledger
//	u32 segments → each: u32 length + bytes (every string is stored this way; u32 so limits
//	need no thought)
//	entries are written in **key order**: bytes generated from the same JSON must be byte-identical,
//	otherwise -check would always report "stale" because of a different map iteration order, and a
//	false alarm like that would make people turn the check off.
//
// Every length read is bounds-checked: this format is a **generated artifact**, but it may come
// from a failed build (a truncated file) or from a repository someone else swapped. An
// out-of-bounds read is a crash, far worse than reporting an error.
const bundleMagic = "NGCAT2\n"

const (
	bundleKindCatalog = 'c'
	bundleKindLedger  = 'l'
)

// ---------- write ----------

type bundleWriter struct{ b []byte }

func (w *bundleWriter) u32(v uint32) {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	w.b = append(w.b, tmp[:]...)
}

func (w *bundleWriter) u8(v byte) { w.b = append(w.b, v) }

func (w *bundleWriter) str(s string) {
	w.u32(uint32(len(s)))
	w.b = append(w.b, s...)
}

// EncodeCatalog encodes one catalog into bundle bytes.
func EncodeCatalog(c Catalog) []byte {
	w := &bundleWriter{b: append([]byte(nil), bundleMagic...)}
	w.u8(bundleKindCatalog)
	w.str(c.Language)
	w.str(c.Source)

	widthKeys := make([]string, 0, len(c.Widths))
	for k := range c.Widths {
		widthKeys = append(widthKeys, k)
	}
	sort.Strings(widthKeys)
	w.u32(uint32(len(widthKeys)))
	for _, k := range widthKeys {
		w.str(k)
		w.u32(uint32(c.Widths[k]))
	}

	ids := make([]string, 0, len(c.Messages))
	for id := range c.Messages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w.u32(uint32(len(ids)))
	for _, id := range ids {
		e := c.Messages[id]
		w.str(id)
		w.str(e.Text)
		w.str(e.One)
		w.str(e.Other)
		w.str(e.Note)
		var flags byte
		if e.Machine {
			flags |= 1
		}
		if e.Reviewed {
			flags |= 2
		}
		w.u8(flags)
	}
	return w.b
}

// EncodeLedger encodes the ledger into bundle bytes.
func EncodeLedger(l Ledger) []byte {
	w := &bundleWriter{b: append([]byte(nil), bundleMagic...)}
	w.u8(bundleKindLedger)

	ids := make([]string, 0, len(l.Messages))
	for id := range l.Messages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w.u32(uint32(len(ids)))
	for _, id := range ids {
		e := l.Messages[id]
		w.str(id)
		args := append([]string(nil), e.Args...)
		sort.Strings(args)
		w.u32(uint32(len(args)))
		for _, a := range args {
			w.str(a)
		}
		w.str(e.One)
		w.str(e.Other)
		w.str(e.Note)
	}
	return w.b
}

// ---------- read ----------

type bundleReader struct {
	b   []byte
	at  int
	err error
}

// hint turns an entry count **from a file** into a capacity hint that is safe to hand to make.
//
// Why it is needed: `make(map[K]V, n)` preallocates by n, and n is a number written in the
// file. A broken or swapped bundle can claim 4 billion entries -- that would demand a huge
// allocation **before the first bounds check takes effect**, and the symptom is an OOM (or a
// plain panic) at startup rather than "this bundle is broken". Here it is clamped by "how many
// bytes are left": each entry is at least perEntry bytes, so a count beyond that is certainly
// fake. What is clamped is the **capacity hint**, not the check -- real out-of-bounds reads are
// still stopped by the check at every step.
func (r *bundleReader) hint(n uint32, perEntry int) int {
	if r.err != nil {
		return 0
	}
	most := uint32((len(r.b) - r.at) / perEntry)
	if n > most {
		return int(most)
	}
	return int(n)
}

// fail records the first out-of-bounds and stops: every later step short-circuits on err, so no
// if is needed at each site.
func (r *bundleReader) fail(what string) {
	if r.err == nil {
		r.err = fmt.Errorf("the message bundle is truncated or corrupt (%s at byte %d)", what, r.at)
	}
}

func (r *bundleReader) u32() uint32 {
	if r.err != nil {
		return 0
	}
	if r.at+4 > len(r.b) {
		r.fail("length")
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.at:])
	r.at += 4
	return v
}

func (r *bundleReader) u8() byte {
	if r.err != nil {
		return 0
	}
	if r.at+1 > len(r.b) {
		r.fail("flag")
		return 0
	}
	v := r.b[r.at]
	r.at++
	return v
}

func (r *bundleReader) str() string {
	if r.err != nil {
		return ""
	}
	n := r.u32()
	if r.err != nil {
		return ""
	}
	// The length is **a number written in the file**: it may be garbage. Clamp it to the remaining
	// bytes before slicing, otherwise one out-of-bounds read is a panic, and a panic here keeps
	// the whole process from starting (the catalog tables are read in Start).
	if int(n) > len(r.b)-r.at {
		r.fail("string length")
		return ""
	}
	s := string(r.b[r.at : r.at+int(n)])
	r.at += int(n)
	return s
}

// DecodeCatalog reads one bundle catalog.
func DecodeCatalog(raw []byte) (Catalog, error) {
	kind, r, err := openBundle(raw)
	if err != nil {
		return Catalog{}, err
	}
	if kind != bundleKindCatalog {
		return Catalog{}, fmt.Errorf("expected a translation bundle, found kind %q", kind)
	}
	out := Catalog{Language: r.str(), Source: r.str()}
	if n := r.u32(); n > 0 {
		out.Widths = make(map[string]int, r.hint(n, 8))
		for i := uint32(0); i < n && r.err == nil; i++ {
			k := r.str()
			out.Widths[k] = int(int32(r.u32()))
		}
	}
	n := r.u32()
	out.Messages = make(map[string]Entry, r.hint(n, 5))
	for i := uint32(0); i < n && r.err == nil; i++ {
		id := r.str()
		e := Entry{Text: r.str(), One: r.str(), Other: r.str(), Note: r.str()}
		flags := r.u8()
		e.Machine = flags&1 != 0
		e.Reviewed = flags&2 != 0
		out.Messages[id] = e
	}
	if r.err != nil {
		return Catalog{}, r.err
	}
	if out.Language == "" {
		return Catalog{}, fmt.Errorf("the translation bundle has no language")
	}
	return out, nil
}

// DecodeLedger reads one bundle ledger.
func DecodeLedger(raw []byte) (Ledger, error) {
	kind, r, err := openBundle(raw)
	if err != nil {
		return Ledger{}, err
	}
	if kind != bundleKindLedger {
		return Ledger{}, fmt.Errorf("expected a ledger bundle, found kind %q", kind)
	}
	n := r.u32()
	out := Ledger{Messages: make(map[string]LedgerEntry, r.hint(n, 5))}
	for i := uint32(0); i < n && r.err == nil; i++ {
		id := r.str()
		var e LedgerEntry
		if na := r.u32(); na > 0 {
			e.Args = make([]string, 0, r.hint(na, 4))
			for j := uint32(0); j < na && r.err == nil; j++ {
				e.Args = append(e.Args, r.str())
			}
		}
		e.One, e.Other, e.Note = r.str(), r.str(), r.str()
		out.Messages[id] = e
	}
	if r.err != nil {
		return Ledger{}, r.err
	}
	return out, nil
}

func openBundle(raw []byte) (byte, *bundleReader, error) {
	if len(raw) < len(bundleMagic)+1 || string(raw[:len(bundleMagic)]) != bundleMagic {
		return 0, nil, fmt.Errorf("not a message bundle (bad magic)")
	}
	r := &bundleReader{b: raw, at: len(bundleMagic)}
	kind := r.u8()
	if r.err != nil {
		return 0, nil, r.err
	}
	return kind, r, nil
}
