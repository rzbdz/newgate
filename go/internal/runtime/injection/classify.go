package injection

import "strings"

// ClassifyModel 把一个真实模型名归类到能力档（tier）。
//
// 这是 docs/11-proxy-native.md §3.4 的档位启发式。命中都会被记录，
// 便于用户核对——**静默错配比失败更糟**：用 light 档跑本该 heavy 的任务，
// 用户只会觉得模型今天变笨了，完全无从追查。
func ClassifyModel(s string) (tier string, exact bool) {
	m := strings.ToLower(s)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "opus"), strings.Contains(m, "-sol"),
		strings.Contains(m, "flagship"):
		return "heavy", true
	case strings.Contains(m, "gemini") && strings.Contains(m, "pro"),
		strings.Contains(m, "vision"):
		return "vision", true
	case strings.Contains(m, "sonnet"), strings.Contains(m, "terra"),
		strings.Contains(m, "medium"), strings.Contains(m, "-large"):
		return "mid", true
	case strings.Contains(m, "luna"), strings.Contains(m, "haiku"),
		strings.Contains(m, "flash"), strings.Contains(m, "lite"),
		strings.Contains(m, "mini"), strings.Contains(m, "small"):
		return "light", true
	}
	return "mid", false // 猜不出来归中档，但标记为非精确，调用方必须打日志
}
