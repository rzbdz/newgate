package rewrite

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

// InsertTopLevelRaw 在顶层对象里**新增**一个 key，插在开头（紧跟 `{`），
// 其余每个字节原样保留。已经存在同名 key 时报错——调用方要先用
// TopLevelRaw 判断，然后决定是插入还是 ReplaceTopLevelRaw，
// 绝不能悄悄产生重复 key。
//
// 为什么插在开头而不是末尾：末尾要处理尾随空白、换行、以及「最后一个值
// 后面到 `}` 之间有什么」，出错概率高；开头只需要判断对象是不是空的。
func InsertTopLevelRaw(body []byte, key string, raw []byte) ([]byte, error) {
	if _, _, err := findTopLevelValue(body, key); err == nil {
		return nil, errDupKey
	}
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil, errNotObject
	}
	j := skipWS(body, i+1)
	if j >= len(body) {
		return nil, errNotObject
	}
	quoted, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	ins := make([]byte, 0, len(quoted)+len(raw)+2)
	ins = append(ins, quoted...)
	ins = append(ins, ':')
	ins = append(ins, raw...)
	if body[j] != '}' { // 对象非空，得补逗号
		ins = append(ins, ',')
	}
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:i+1]...)
	out = append(out, ins...)
	out = append(out, body[i+1:]...)
	return out, nil
}

// EnsureArrayItemField 给顶层数组（messages 等）里满足 match 的每个对象元素
// 补上一个字段——**只在该元素还没有这个字段时**补，已有的一律不动。
//
// 依然是纯字节手术：把所有插入点先收集起来，再一次性拼接。除了插入的那几段，
// 每条消息的 content 块、cache_control、Unicode 转义形式、缩进都逐字节不变。
// 这一点是硬要求：把整个 body 做一次 JSON 往返会重排 key、把大整数变成
// float64、压掉空白——转发一个请求就该是转发。
//
// match 收到的是这条元素完整的原始字节（一个 JSON 对象），可以直接用
// TopLevelString(item, "role") 之类去判断。
// 返回补了几条；数组不存在或元素不是对象时返回 0 而不报错的情况见 err。
func EnsureArrayItemField(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	if len(arr) == 0 || arr[0] != '[' {
		return body, 0, errNotArray
	}
	quoted, err := json.Marshal(field)
	if err != nil {
		return body, 0, err
	}

	type point struct {
		off   int  // body 里的绝对偏移（元素 `{` 之后）
		comma bool // 元素非空时要补逗号
	}
	var points []point

	i := 1
	for {
		i = skipWS(arr, i)
		if i >= len(arr) || arr[i] == ']' {
			break
		}
		if arr[i] == ',' {
			i++
			continue
		}
		vs := i
		ve, ok := scanValue(arr, i)
		if !ok {
			return body, 0, errNotObject
		}
		item := arr[vs:ve]
		i = ve

		if len(item) == 0 || item[0] != '{' {
			continue // 元素不是对象：不是我们该管的形状，跳过
		}
		if _, _, err := findTopLevelValue(item, field); err == nil {
			continue // 已经有了，一个字节都不碰
		}
		if match != nil && !match(item) {
			continue
		}
		j := skipWS(item, 1)
		points = append(points, point{off: s + vs + 1, comma: j < len(item) && item[j] != '}'})
	}

	if len(points) == 0 {
		return body, 0, nil // 没东西要补：调用方保持原字节
	}

	out := make([]byte, 0, len(body)+len(points)*(len(quoted)+len(rawVal)+2))
	prev := 0
	for _, p := range points {
		out = append(out, body[prev:p.off]...)
		out = append(out, quoted...)
		out = append(out, ':')
		out = append(out, rawVal...)
		if p.comma {
			out = append(out, ',')
		}
		prev = p.off
	}
	out = append(out, body[prev:]...)
	return out, len(points), nil
}

var (
	errNotObject = errors.New("请求体顶层不是 JSON 对象")
	errNoKey     = errors.New("顶层找不到该字段")
	errNotString = errors.New("该字段的值不是字符串")
	errNotArray  = errors.New("该字段的值不是数组")
	errDupKey    = errors.New("顶层已经有这个字段了")
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
