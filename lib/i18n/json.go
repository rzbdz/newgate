package i18n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Two kinds of file, one parser:
//
//	ledger catalogs/ledger.json   generated (written by tools/i18n extract, checked by -check).
//	                              Records which messages exist in the source, with what arguments.
//	catalog catalogs/<lang>.json  translation table. The key is the source-language text itself.
//
// Why the ledger is generated and the catalogs are not: the ledger can be **derived** from the
// source (deriving it wrong is a bug and should go red in CI), the catalogs cannot be derived
// (they are human output, which tools may only **append** to, never overwrite).

// A catalog file looks like this (keys sorted, stable output, easy to diff and review):
//
//	{
//	  "meta": { "language": "zh-Hans", "source": "en", "widths": { "label": 8 } },
//	  "messages": {
//	    "Proxy": "Proxy",
//	    "Saved {n} file": { "one": "Se guardó {n} archivo", "other": "Se guardaron {n} archivos",
//	                        "machine": true, "reviewed": false },
//	    "Kept {n} keys": { "text": "Se conservaron {n} claves", "note": "translator context" }
//	  }
//	}
//
// A translation with only a body is written as a **bare string** (the vast majority), and only
// when it needs plurals / notes / machine-translation flags is it written as an object -- the
// file reads like a lookup table rather than a pile of boilerplate fields.
type fileJSON struct {
	Meta     metaJSON                   `json:"meta"`
	Messages map[string]json.RawMessage `json:"messages"`
}

type metaJSON struct {
	Language string         `json:"language"`
	Source   string         `json:"source,omitempty"`
	Widths   map[string]int `json:"widths,omitempty"`
}

type entryJSON struct {
	Text     string `json:"text,omitempty"`
	One      string `json:"one,omitempty"`
	Other    string `json:"other,omitempty"`
	Note     string `json:"note,omitempty"`
	Machine  bool   `json:"machine,omitempty"`
	Reviewed bool   `json:"reviewed,omitempty"`
}

// Ledger file: keys are the source-language text, values only record "where, with what
// arguments, how the plural is written, human notes". It **contains no translations** -- the
// source-language text is the sentence in the source, and copying one in here would inevitably
// drift.
type ledgerJSON struct {
	Note     string                     `json:"note"`
	Messages map[string]ledgerEntryJSON `json:"messages"`
}

type ledgerEntryJSON struct {
	Args  []string `json:"args,omitempty"`
	Other string   `json:"other,omitempty"` // 复数消息的复数形式
	Note  string   `json:"note,omitempty"`  // 人写的，extract 不许覆盖
}

// ParseCatalog parses one **catalog**. A misspelled field name is an error
// (DisallowUnknownFields): a misspelled `"machien": true` silently ignored has the symptom "I
// marked it as machine translated, why isn't it marked".
func ParseCatalog(raw []byte) (Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f fileJSON
	if err := dec.Decode(&f); err != nil {
		return Catalog{}, fmt.Errorf("the translation file does not parse: %w", err)
	}
	if f.Meta.Language == "" {
		return Catalog{}, fmt.Errorf("the translation file has no meta.language")
	}
	out := Catalog{
		Language: f.Meta.Language,
		Source:   f.Meta.Source,
		Widths:   f.Meta.Widths,
		Messages: make(map[string]Entry, len(f.Messages)),
	}
	for id, rawEntry := range f.Messages {
		e, err := parseEntry(rawEntry)
		if err != nil {
			return Catalog{}, fmt.Errorf("message %q: %w", id, err)
		}
		out.Messages[id] = e
	}
	return out, nil
}

func parseEntry(raw json.RawMessage) (Entry, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Entry{}, fmt.Errorf("empty entry")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return Entry{}, err
		}
		return Entry{Text: s}, nil
	}
	var ej entryJSON
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ej); err != nil {
		return Entry{}, err
	}
	if ej.Text == "" && ej.One == "" && ej.Other == "" {
		return Entry{}, fmt.Errorf("the entry has neither text nor one/other")
	}
	return Entry{
		Text: ej.Text, One: ej.One, Other: ej.Other,
		Note: ej.Note, Machine: ej.Machine, Reviewed: ej.Reviewed,
	}, nil
}

// MarshalCatalog renders a catalog file (keys sorted: random map iteration order would make
// every generated diff wobble, same reasoning as tools/genmodules).
func MarshalCatalog(c Catalog) ([]byte, error) {
	f := fileJSON{
		Meta:     metaJSON{Language: c.Language, Source: c.Source, Widths: c.Widths},
		Messages: make(map[string]json.RawMessage, len(c.Messages)),
	}
	for id, e := range c.Messages {
		raw, err := marshalEntry(e)
		if err != nil {
			return nil, fmt.Errorf("message %q: %w", id, err)
		}
		f.Messages[id] = raw
	}
	return encode(f)
}

func marshalEntry(e Entry) (json.RawMessage, error) {
	if !e.plural() && e.Note == "" && !e.Machine && !e.Reviewed {
		return json.Marshal(e.Text)
	}
	return json.Marshal(entryJSON{
		Text: e.Text, One: e.One, Other: e.Other,
		Note: e.Note, Machine: e.Machine, Reviewed: e.Reviewed,
	})
}

// ParseLedger parses the ledger.
func ParseLedger(raw []byte) (Ledger, error) {
	var f ledgerJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Ledger{}, fmt.Errorf("the ledger does not parse: %w", err)
	}
	out := Ledger{Messages: make(map[string]LedgerEntry, len(f.Messages))}
	for id, e := range f.Messages {
		out.Messages[id] = LedgerEntry{Args: e.Args, Other: e.Other, Note: e.Note}
	}
	return out, nil
}

// MarshalLedger renders the ledger (also keys sorted + fixed indent).
func MarshalLedger(l Ledger) ([]byte, error) {
	f := ledgerJSON{
		Note:     "Generated by tools/i18n extract. Do not edit by hand — change the T(...) call in the source instead.",
		Messages: make(map[string]ledgerEntryJSON, len(l.Messages)),
	}
	for id, e := range l.Messages {
		f.Messages[id] = ledgerEntryJSON{Args: e.Args, Other: e.Other, Note: e.Note}
	}
	return encode(f)
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 文案里会有 < > &，转义成 &lt; 就没法读了
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// OverlayDir is the disk override directory: `<root>/locale/`.
//
// Why overrides are allowed: translations are **the thing users are most likely to catch an
// error in on the spot** (a clumsy translation, the user sees it before the translator does).
// Letting them edit one JSON and have it take effect beats making them wait for the next
// release -- and it matches this project's usual approach (drop a .kv in mappings/ and it takes
// effect).
func OverlayDir(root string) string { return filepath.Join(root, "locale") }

// OverlayPath is where the override file for one language lives.
func OverlayPath(dir, tag string) string { return filepath.Join(dir, tag+".json") }

// readOverlay reads the disk override. File missing, unreadable, undecodable -- **none of them
// error**: it is a nice-to-have path, and breaking it should not block startup. But a decode
// failure has to leave a hint -- silently ignoring it turns "why did my edit not take effect"
// into an unsolvable investigation.
func readOverlay(dir, tag string) (Catalog, bool) {
	if dir == "" || tag == "" {
		return Catalog{}, false
	}
	raw, err := os.ReadFile(OverlayPath(dir, tag))
	if err != nil {
		return Catalog{}, false
	}
	c, err := ParseCatalog(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: ignoring the language overlay %s (it does not parse): %v\n", OverlayPath(dir, tag), err)
		return Catalog{}, false
	}
	c.Builtin = false
	return c, true
}

// placeholderName decides whether the word inside `{...}` is a **placeholder name**: only an
// identifier shape counts.
//
// Why not treat any braces as placeholders: copy contains **machine markers**, and JSON itself
// carries braces -- in `injected thinking:{"type":"disabled"} ({why})` that pair of braces is
// request-body bytes, not a slot to fill. Judging by identifier shape, the two never collide;
// argument names are literally the keys written as literals on the Go side, and identifier
// shape is their natural convention (see the A type comment).
func placeholderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z'):
		case '0' <= c && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Placeholders extracts the placeholder names in one piece of copy (deduplicated, sorted).
//
// It has two users: tools/i18n checking "the placeholder set of the translation matches the
// source" (a missing `{dir}` is a bad message -- the UI would show `{dir}` forever), and
// checking that the arguments given at the call site line up.
func Placeholders(text string) []string {
	seen := map[string]bool{}
	for i := 0; i < len(text); {
		if text[i] != '{' {
			i++
			continue
		}
		end := strings.IndexByte(text[i:], '}')
		if end < 0 {
			break
		}
		name := text[i+1 : i+end]
		if placeholderName(name) {
			seen[name] = true
		}
		i += end + 1
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EntryPlaceholders takes the union of placeholders across every form of one translation
// (singular and plural).
func EntryPlaceholders(e Entry) []string {
	seen := map[string]bool{}
	for _, t := range []string{e.Text, e.One, e.Other} {
		for _, p := range Placeholders(t) {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SortedKeys sorts the keys of a message table (ledger or catalog).
func SortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
