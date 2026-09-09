// Package store 里的 kv.go：profile 的 KV 文本格式（mappings/*.kv）。
//
// 为什么要有第二种格式：嵌套 JSON 手写容易错（逗号、括号、引号），
// 而 profile 恰恰是用户最常手改的东西——换个模型名、加个变体。KV 一行
// 一个字段，肉眼可查：
//
//	# mappings/kimi-fast.kv
//	extends=kimi
//	excluded=true
//	desc=Kimi 主循环换高速版
//	mid=kimi-k2.7-code-highspeed
//
// 语法：`key=value` 一行一个；`#` 开头是注释；空行忽略。档位缩写
// （heavy/mid/light/vision）各占一个 key，值是 `provider/model` 或裸模型
// 名（extends 时从 base 同档位借 provider）；多个候选用逗号分隔。
// 未知 key **报错**，不静默忽略（docs/06 §8 的老规矩）。
//
// 优先级：同名 .kv 和 .json 并存时 **.kv 赢**——kv 只会被人刻意创建，
// 它出现就是最新的意图。`newgate profile kv <名> --write` 转换时会把
// .json 改名成 .bak，正常用不会踩到并存。
package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate/go/internal/core/domain"
)

// tierKeys 档位缩写 → roles key。
var tierKeys = map[string]bool{"heavy": true, "mid": true, "light": true, "vision": true}

// ParseProfileKV 把 KV 文本解析成 Profile（Extends 的合并不在这做，
// 由 LoadProfile 负责）。
func ParseProfileKV(text string) (*domain.Profile, error) {
	p := &domain.Profile{Roles: map[string]domain.Candidates{}}
	for ln, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("第 %d 行不像 key=value: %q", ln+1, line)
		}
		key, val := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])
		if val == "" {
			return nil, fmt.Errorf("第 %d 行 %s= 的值是空的", ln+1, key)
		}
		var err error
		switch {
		case key == "name":
			p.Name = val
		case key == "extends":
			p.Extends = val
		case key == "desc" || key == "description":
			p.Description = val
		case key == "prio" || key == "priority":
			var n int
			if n, err = strconv.Atoi(val); err == nil {
				v := n
				p.Priority = &v
			}
		case key == "excluded":
			p.Excluded, err = parseBool(val)
		case key == "pinned":
			p.Pinned, err = parseBool(val)
		case key == "window":
			p.ContextWindow, err = strconv.Atoi(val)
		case key == "compact":
			p.AutoCompactWindow, err = strconv.Atoi(val)
		case key == "fallback":
			var c domain.Candidates
			if c, err = parseCandidates(val); err == nil {
				p.Fallback = &c[0]
			}
		case tierKeys[key]:
			var c domain.Candidates
			if c, err = parseCandidates(val); err == nil {
				p.Roles[key] = c
			}
		case strings.HasPrefix(key, "role."):
			// 任意档位名（含 "*"）：role.search=... / role.*=...
			var c domain.Candidates
			if c, err = parseCandidates(val); err == nil {
				p.Roles[key[len("role."):]] = c
			}
		default:
			return nil, fmt.Errorf("第 %d 行不认识的 key %q（写错字会被静默忽略，" +
				"所以这里宁可报错）", ln+1, key)
		}
		if err != nil {
			return nil, fmt.Errorf("第 %d 行 %s=%s: %v", ln+1, key, val, err)
		}
	}
	return p, nil
}

func parseBool(v string) (bool, error) {
	switch v {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("布尔值只认 true/false，不猜")
}

// parseCandidates `provider/model` 或裸模型名，逗号分隔多个候选。
// 裸名的 provider 由 Extends 合并时补（fillBareProviders）。
func parseCandidates(val string) (domain.Candidates, error) {
	var out domain.Candidates
	for _, item := range strings.Split(val, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if i := strings.IndexByte(item, '/'); i >= 0 {
			out = append(out, domain.Binding{Provider: item[:i], Model: item[i+1:]})
		} else {
			out = append(out, domain.Binding{Model: item})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有有效的候选")
	}
	return out, nil
}

// SerializeProfileKV 把 profile 写回 KV 文本（转换用）。只写**非零**字段，
// 配合 Extends 就是紧凑的变体声明；档位多候选用逗号展开。
// name 不写——文件名就是名字。
func SerializeProfileKV(p *domain.Profile) string {
	var sb strings.Builder
	put := func(k, v string) { fmt.Fprintf(&sb, "%s=%s\n", k, v) }
	if p.Extends != "" {
		put("extends", p.Extends)
	}
	if p.Description != "" {
		put("desc", p.Description)
	}
	if p.Priority != nil {
		put("prio", strconv.Itoa(*p.Priority))
	}
	if p.Excluded {
		put("excluded", "true")
	}
	if p.Pinned {
		put("pinned", "true")
	}
	if p.ContextWindow > 0 {
		put("window", strconv.Itoa(p.ContextWindow))
	}
	if p.AutoCompactWindow > 0 {
		put("compact", strconv.Itoa(p.AutoCompactWindow))
	}
	for _, tier := range domain.Roles {
		if c, ok := p.Roles[tier]; ok && len(c) > 0 {
			items := make([]string, len(c))
			for i, b := range c {
				items[i] = b.String()
			}
			put(tier, strings.Join(items, ", "))
		}
	}
	// 4 个标准档位之外的 roles key（含 "*"）走 role.<名>=，保证无损往返
	for _, k := range sortedRoleKeys(p.Roles) {
		if tierKeys[k] {
			continue
		}
		items := make([]string, len(p.Roles[k]))
		for i, b := range p.Roles[k] {
			items[i] = b.String()
		}
		put("role."+k, strings.Join(items, ", "))
	}
	if p.Fallback != nil {
		put("fallback", p.Fallback.String())
	}
	return sb.String()
}

func sortedRoleKeys(m map[string]domain.Candidates) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
