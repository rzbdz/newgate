package i18n

import "strings"

// 语言标签的归一化与匹配。
//
// 为什么要自己写这二十行：环境变量里的语言有太多写法——`zh_CN.UTF-8`、
// `zh-CN`、`zh`、`C`、`POSIX`、空值、`en_US.utf8`。拿它们直接当 map 键去查，
// 中文用户会因为一个 `.UTF-8` 后缀而拿到英文。这里只做够用的一小步：
// 去编码、下划线转连字符、语言子标签小写、其余首字母大写，然后按
// 「精确 → 同语言 → 中文脚本特判」三级匹配。

// Normalize 归一化一个语言标签；判不出（C/POSIX/空）时返回空串。
//
// 空串的含义是「没有意见」，不是「英语」——调用方拿到空串应当继续往下一条
// 线索走（还有别的环境变量、还有配置、最后才是源语言）。
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
	// 大小写按 BCP-47 的惯例摆：语言小写、两字母地区大写（CN/TW）、四位脚本
	// 首字母大写（Hans/Hant/Latn）。不这么做的话 `zh-CN` 会变成 `zh-Cn`，
	// 而它会被拿去当覆盖文件名、也要跟目录里的 tag 比——一次大小写之差就是一小时排查。
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

// Match 在可选语言里挑一个最合适的；挑不出返回空串。
//
// 三级：**精确**（zh-Hant 就是 zh-Hant）→ **同语言 + 脚本特判**（zh-CN 落到
// zh-Hans，zh-TW/zh-HK 落到 zh-Hant）→ **裸语言**（en-US 落到 en）。
// 中文必须特判：简体/繁体不是一个「地区变体」那么简单，同一句话在两边读起来
// 不一样，而 `zh` 这个 tag 本身不说明用哪一套。
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

// primary 取语言子标签（`zh-Hans-CN` → `zh`）。
func primary(tag string) string {
	if i := strings.IndexByte(tag, '-'); i >= 0 {
		return strings.ToLower(tag[:i])
	}
	return strings.ToLower(tag)
}

// scriptOf 判断中文用哪套字：地区优先，其次显式脚本，都没有时按简体
// （使用者里简体占多数，猜错的那一半人至少还能读）。
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
