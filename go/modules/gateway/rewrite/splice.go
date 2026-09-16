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

	return EnsureArrayItemFieldFunc(body, arrayKey, field,
		func([]byte) []byte { return rawVal }, match)
}

// EnsureArrayItemFieldFunc 同 EnsureArrayItemField，但值**逐条算**：
// val 收到这条元素的原始字节，返回要插的值，返回 nil 表示这条跳过。
//
// 为什么需要逐条：推理内容回填时每条 assistant 消息要插的是**它自己那轮**
// 的推理，不是同一个常量。给所有消息插同一段推理，等于把别人的思考塞给
// 这一轮，比补空串更糟。
func EnsureArrayItemFieldFunc(body []byte, arrayKey, field string,
	val func(item []byte) []byte, match func(item []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, 0, errNotArray
	}
	quoted, err := json.Marshal(field)
	if err != nil {
		return body, 0, err
	}

	type point struct {
		off   int    // body 里的绝对偏移（元素 `{` 之后）
		raw   []byte // 这条元素要插的值
		comma bool   // 元素非空时要补逗号
	}
	var points []point

	for _, sp := range spans {
		item := arr[sp[0]:sp[1]]
		if len(item) == 0 || item[0] != '{' {
			continue // 元素不是对象：不是我们该管的形状，跳过
		}
		if _, _, err := findTopLevelValue(item, field); err == nil {
			continue // 已经有了，一个字节都不碰
		}
		if match != nil && !match(item) {
			continue
		}
		raw := val(item)
		if raw == nil {
			continue
		}
		j := skipWS(item, 1)
		points = append(points, point{
			off:   s + sp[0] + 1,
			raw:   raw,
			comma: j < len(item) && item[j] != '}',
		})
	}

	if len(points) == 0 {
		return body, 0, nil // 没东西要补：调用方保持原字节
	}

	out := make([]byte, 0, len(body)+len(points)*(len(quoted)+16))
	prev := 0
	for _, p := range points {
		out = append(out, body[prev:p.off]...)
		out = append(out, quoted...)
		out = append(out, ':')
		out = append(out, p.raw...)
		if p.comma {
			out = append(out, ',')
		}
		prev = p.off
	}
	out = append(out, body[prev:]...)
	return out, len(points), nil
}

// EnsureArrayItemArrayHead 往顶层数组 arrayKey（messages）里满足 match 的每个
// 对象元素的**子数组**字段 field（content）的**开头**插入 rawVal，
// 且只在 need(childArr) 为真时插。返回插了几条。
//
// 为什么要单独一个原语：EnsureArrayItemField 只能往元素对象上补顶层字段，
// 够不着元素里面那个 content 数组。而 Anthropic 协议里 thinking 是
// content[] 的一个**块**，且必须排在 text / tool_use 之前——位置是协议的
// 一部分，不能随便追加到末尾。
//
// 依旧是纯字节手术：先把所有插入点收集齐，再一次拼接。除了插进去的那几段，
// 每个块的 cache_control、tool_use 的 input、缩进、转义形式逐字节不变。
//
// 跳过（不算错，也不算改）：元素不是对象、没有 field 字段、field 的值不是
// 数组（content 是纯字符串时就是这样）。
func EnsureArrayItemArrayHead(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool, need func(childArr []byte) bool) ([]byte, int, error) {

	return EnsureArrayItemArrayHeadFunc(body, arrayKey, field,
		func([]byte) []byte { return rawVal }, match, need)
}

// EnsureArrayItemArrayHeadFunc 同上，但值逐条算（val 返回 nil = 跳过）。
// 理由同 EnsureArrayItemFieldFunc：每条 assistant 消息要插的是它自己那轮的
// thinking 块。
func EnsureArrayItemArrayHeadFunc(body []byte, arrayKey, field string,
	val func(item []byte) []byte,
	match func(item []byte) bool, need func(childArr []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, 0, errNotArray
	}

	type point struct {
		off   int    // body 里的绝对偏移（子数组 `[` 之后）
		raw   []byte // 这条元素要插的块
		comma bool   // 子数组非空时要补逗号
	}
	var points []point

	for _, sp := range spans {
		item := arr[sp[0]:sp[1]]
		if len(item) == 0 || item[0] != '{' {
			continue
		}
		if match != nil && !match(item) {
			continue
		}
		cs, ce, err := findTopLevelValue(item, field)
		if err != nil {
			continue // 没有 content 字段
		}
		child := item[cs:ce]
		if len(child) == 0 || child[0] != '[' {
			continue // content 是字符串：没有块可插
		}
		if need != nil && !need(child) {
			continue
		}
		raw := val(item)
		if raw == nil {
			continue
		}
		j := skipWS(child, 1)
		points = append(points, point{
			off:   s + sp[0] + cs + 1,
			raw:   raw,
			comma: j < len(child) && child[j] != ']',
		})
	}

	if len(points) == 0 {
		return body, 0, nil
	}

	out := make([]byte, 0, len(body)+len(points)*32)
	prev := 0
	for _, p := range points {
		out = append(out, body[prev:p.off]...)
		out = append(out, p.raw...)
		if p.comma {
			out = append(out, ',')
		}
		prev = p.off
	}
	out = append(out, body[prev:]...)
	return out, len(points), nil
}

// AppendLastArrayItemArray 往顶层数组最后一个对象元素的子数组末尾追加 rawVal。
// match 用来确认最后一项确实是调用方要处理的形状。只插入这一段字节，其余
// 请求（包括大整数、字段顺序和未知字段）保持原样。
func AppendLastArrayItemArray(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool) ([]byte, bool, error) {
	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, false, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, false, errNotArray
	}
	if len(spans) == 0 {
		return body, false, nil
	}
	sp := spans[len(spans)-1]
	item := arr[sp[0]:sp[1]]
	if len(item) == 0 || item[0] != '{' || (match != nil && !match(item)) {
		return body, false, nil
	}
	cs, ce, err := findTopLevelValue(item, field)
	if err != nil {
		return body, false, err
	}
	child := item[cs:ce]
	if len(child) == 0 || child[0] != '[' {
		return body, false, errNotArray
	}
	close := len(child) - 1
	for close > 0 {
		switch child[close] {
		case ' ', '\t', '\r', '\n':
			close--
			continue
		}
		break
	}
	if child[close] != ']' {
		return body, false, errNotArray
	}
	j := skipWS(child, 1)
	ins := rawVal
	if j < len(child) && child[j] != ']' {
		ins = append([]byte(","), rawVal...)
	}
	off := s + sp[0] + cs + close
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:off]...)
	out = append(out, ins...)
	out = append(out, body[off:]...)
	return out, true, nil
}