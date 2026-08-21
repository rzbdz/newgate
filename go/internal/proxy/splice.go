package proxy

import (
	"encoding/json"
	"errors"
)

// ReplaceTopLevelString 在原始 JSON 字节里定位顶层 key 的字符串值，
// 只替换那一段，其余**每一个字节**原样保留——包括 key 顺序、缩进、空白、
// 数字字面量、Unicode 转义形式。
//
// 为什么不能用 Unmarshal/Marshal 往返：
//   - map[string]interface{} 会把整数变 float64（9007199254740993 → ...92）、
//     1e10 变 10000000000、所有 key 重排
//   - map[string]json.RawMessage 保住了嵌套字节，但顶层 key 仍被重排、空白仍被压掉
//
// 转发一个请求就该是转发，不该顺手重写它。
func ReplaceTopLevelString(body []byte, key, newVal string) ([]byte, error) {
	start, end, err := findTopLevelStringValue(body, key)
	if err != nil {
		return nil, err
	}
	quoted, err := json.Marshal(newVal)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)-(end-start)+len(quoted))
	out = append(out, body[:start]...)
	out = append(out, quoted...)
	out = append(out, body[end:]...)
	return out, nil
}

// ReplaceTopLevelRaw 把顶层 key 的整个值换成 newRaw（原始 JSON 字节），
// 其余字节原样保留。用于只重写 tools 而不动 messages。
func ReplaceTopLevelRaw(body []byte, key string, newRaw []byte) ([]byte, error) {
	start, end, err := findTopLevelValue(body, key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)-(end-start)+len(newRaw))
	out = append(out, body[:start]...)
	out = append(out, newRaw...)
	out = append(out, body[end:]...)
	return out, nil
}

// TopLevelRaw 只读地取出顶层 key 的原始值字节。
func TopLevelRaw(body []byte, key string) ([]byte, bool) {
	s, e, err := findTopLevelValue(body, key)
	if err != nil {
		return nil, false
	}
	return body[s:e], true
}

var (
	errNotObject = errors.New("请求体顶层不是 JSON 对象")
	errNoKey     = errors.New("顶层找不到该字段")
	errNotString = errors.New("该字段的值不是字符串")
)

// findTopLevelStringValue 同 findTopLevelValue，但要求值是字符串。
func findTopLevelStringValue(body []byte, key string) (int, int, error) {
	s, e, err := findTopLevelValue(body, key)
	if err != nil {
		return 0, 0, err
	}
	if body[s] != '"' {
		return 0, 0, errNotString
	}
	return s, e, nil
}

// findTopLevelValue 返回顶层 key 的值在 body 中的字节区间 [start,end)。
// 只看深度 1 的键，所以嵌套在 tools 里的同名 "model" 不会被误伤。
func findTopLevelValue(body []byte, key string) (int, int, error) {
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return 0, 0, errNotObject
	}
	i++ // 进入对象，深度 = 1

	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return 0, 0, errNoKey
		}
		if body[i] == '}' {
			return 0, 0, errNoKey
		}
		if body[i] == ',' {
			i++
			continue
		}
		if body[i] != '"' {
			return 0, 0, errNotObject
		}

		// 读 key
		kStart := i
		kEnd, ok := scanString(body, i)
		if !ok {
			return 0, 0, errNotObject
		}
		var k string
		if err := json.Unmarshal(body[kStart:kEnd], &k); err != nil {
			return 0, 0, errNotObject
		}
		i = skipWS(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return 0, 0, errNotObject
		}
		i = skipWS(body, i+1)
		if i >= len(body) {
			return 0, 0, errNotObject
		}

		vStart := i
		vEnd, ok := scanValue(body, i)
		if !ok {
			return 0, 0, errNotObject
		}
		if k == key {
			return vStart, vEnd, nil
		}
		i = vEnd
	}
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString 从 b[i]=='"' 开始，返回闭合引号之后的位置。
func scanString(b []byte, i int) (int, bool) {
	if i >= len(b) || b[i] != '"' {
		return 0, false
	}
	i++
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2 // 跳过转义序列（\uXXXX 的后续字符都不是 " 或 \，安全）
		case '"':
			return i + 1, true
		default:
			i++
		}
	}
	return 0, false
}

// scanValue 返回 b[i] 开始的这个 JSON 值之后的位置。
// 只需要正确划界，不需要校验合法性——校验是上游的事，我们只管别改坏。
func scanValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch b[i] {
	case '"':
		return scanString(b, i)
	case '{', '[':
		open, close := b[i], byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				n, ok := scanString(b, i)
				if !ok {
					return 0, false
				}
				i = n
				continue
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return 0, false
	default:
		// 数字 / true / false / null：扫到分隔符为止
		for i < len(b) {
			switch b[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i, true
			}
			i++
		}
		return i, true
	}
}

// TopLevelString 只读地取出顶层某个字符串字段。
func TopLevelString(body []byte, key string) (string, bool) {
	s, e, err := findTopLevelStringValue(body, key)
	if err != nil {
		return "", false
	}
	var v string
	if json.Unmarshal(body[s:e], &v) != nil {
		return "", false
	}
	return v, true
}

// TopLevelBool 只读地取出顶层某个布尔字段（stream 用）。
func TopLevelBool(body []byte, key string) bool {
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return false
	}
	i++
	for {
		i = skipWS(body, i)
		if i >= len(body) || body[i] == '}' {
			return false
		}
		if body[i] == ',' {
			i++
			continue
		}
		if body[i] != '"' {
			return false
		}
		kStart := i
		kEnd, ok := scanString(body, i)
		if !ok {
			return false
		}
		var k string
		if json.Unmarshal(body[kStart:kEnd], &k) != nil {
			return false
		}
		i = skipWS(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return false
		}
		i = skipWS(body, i+1)
		vStart := i
		vEnd, ok := scanValue(body, i)
		if !ok {
			return false
		}
		if k == key {
			return string(body[vStart:vEnd]) == "true"
		}
		i = vEnd
	}
}
