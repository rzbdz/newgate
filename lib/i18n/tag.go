package i18n

import "strings"

// Normalization and matching of language tags.
//
// Why write these twenty lines yourself: language values in environment variables come in too
// many spellings -- `zh_CN.UTF-8`, `zh-CN`, `zh`, `C`, `POSIX`, empty, `en_US.utf8`. Use them
// directly as map keys and Chinese users get English because of a `.UTF-8` suffix. This only
// does the small step that is enough: drop the encoding, turn underscores into hyphens,
// lowercase the language subtag, capitalize the rest, then match in three levels: "exact ->
// same language -> Chinese script special case".

// Normalize normalizes a language tag; returns an empty string when it cannot tell
// (C/POSIX/empty).
//
// An empty string means "no opinion", not "English" -- on receiving an empty string the caller
// should keep following the next clue (there are more environment variables, there is
// configuration, and the source language comes last).
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '.'); i >= 0 { // zh_CN.UTF-8 → zh_CN
		s = s[:i]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 { // sr_RS@latin
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "_", "-")
	switch strings.ToLower(s) {
	case "c", "posix": // 「没意见」的两种传统写法
		return ""
	}
	// Case follows BCP-47 convention: language lowercase, two-letter region uppercase (CN/TW),
	// four-letter script capitalized (Hans/Hant/Latn). Without that `zh-CN` would become `zh-Cn`,
	// and it gets used as an overlay file name and has to be compared with the tag in a catalog --
	// one case difference is an hour of investigation.
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		switch {
		case i == 0:
			parts[i] = strings.ToLower(p)
		case len(p) == 2, len(p) == 3 && isDigits(p):
			parts[i] = strings.ToUpper(p) // 地区
		default:
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:]) // 脚本
		}
	}
	return strings.Join(parts, "-")
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Match picks the best fit among the available languages; returns an empty string when there is
// none.
//
// Three levels: **exact** (zh-Hant is zh-Hant) -> **same language + script special case**
// (zh-CN lands on zh-Hans, zh-TW/zh-HK land on zh-Hant) -> **bare language** (en-US lands on
// en). Chinese needs the special case: simplified/traditional is not just a "regional variant",
// the same sentence reads differently on the two sides, and the tag `zh` itself does not say
// which one to use.
func Match(want string, avail []string) string {
	w := Normalize(want)
	if w == "" {
		return ""
	}
	for _, a := range avail {
		if strings.EqualFold(a, w) {
			return a
		}
	}

	lang := primary(w)
	var bare, hans, hant string
	for _, a := range avail {
		if primary(a) != lang {
			continue
		}
		switch lower := strings.ToLower(a); {
		case a == lang:
			bare = a
		case strings.HasSuffix(lower, "-hans"):
			hans = a
		case strings.HasSuffix(lower, "-hant"):
			hant = a
		}
	}
	if lang == "zh" {
		if scriptOf(w) == "Hant" {
			if hant != "" {
				return hant
			}
			if hans != "" {
				return hans
			}
		} else {
			if hans != "" {
				return hans
			}
			if hant != "" {
				return hant
			}
		}
	}
	if bare != "" {
		return bare
	}
	return ""
}

// primary takes the language subtag (`zh-Hans-CN` → `zh`).
func primary(tag string) string {
	if i := strings.IndexByte(tag, '-'); i >= 0 {
		return strings.ToLower(tag[:i])
	}
	return strings.ToLower(tag)
}

// scriptOf decides which Chinese script to use: region first, then an explicit script, and
// simplified when there is neither (simplified is the majority among users, and the half that
// is guessed wrong can at least still read it).
func scriptOf(tag string) string {
	parts := strings.Split(tag, "-")
	for _, p := range parts[1:] {
		switch strings.ToLower(p) {
		case "hant", "tw", "hk", "mo":
			return "Hant"
		case "hans", "cn", "sg", "my":
			return "Hans"
		}
	}
	return "Hans"
}
