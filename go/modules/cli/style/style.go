// Package style 是三个前端壳共用的**排版原语**：颜色、对齐、分区、表格。
//
// 为什么值得单独一层：CLI 的输出是「详略得当」这件事的唯一载体——用户看
// 一眼要能拿到结论，出问题时才展开细节。以前每个命令各写各的
// fmt.Printf("%-22s ...")，宽窄不一、中英混排错位（Go 的 %-22s 按**字节**
// 补空格，中文一个字 3 字节、显示却占 2 列，整张表都会歪）、颜色写死在
// 字符串里。统一到这里之后，「长什么样」只有一处定义。
//
// 只管排版，不含任何业务判断——三个壳（CLI / TUI / Web）都可以依赖它。
package style

import (
	"os"
	"strings"
	"unicode/utf8"
)

// ---------- 颜色 ----------

// 语义色，别在调用点写裸 ANSI。
const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cBlue   = "\033[34m"
	cCyan   = "\033[36m"
)

// colorEnabled 输出到终端时才上色。
//
// 判据三条，优先级从高到低：
//
//	NEWGATE_COLOR=always|never   显式说了算（写测试、录屏用）
//	NO_COLOR 非空                用户/生态的通用约定
//	stdout 是字符设备            重定向到文件或管道时自动褪色
//
// 第三条是最要紧的：`newgate status > 报告.txt` 以前会把一堆 \033[1m 写进
// 文件里，用户复制出来的东西是坏的。
var colorEnabled = func() bool {
	switch os.Getenv("NEWGATE_COLOR") {
	case "always", "1":
		return true
	case "never", "0":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}()

func paint(code, s string) string {
	if s == "" || !colorEnabled {
		return s
	}
	return code + s + cReset
}

// TTY stdout 是不是终端。分页、进度条这类「只有交互时才该发生」的行为
// 都拿它当判据——重定向到文件时必须退化成老实打印。
func TTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func Bold(s string) string   { return paint(cBold, s) }
func Dim(s string) string    { return paint(cDim, s) }
func Red(s string) string    { return paint(cRed, s) }
func Green(s string) string  { return paint(cGreen, s) }
func Yellow(s string) string { return paint(cYellow, s) }
func Cyan(s string) string   { return paint(cCyan, s) }

// ---------- 符号 ----------

// 四种状态标记。整份 CLI 只认这四个，别再造第五个。
//
//	OK   做到了 / 查过没问题
//	Bad  出错了 / 不可用
//	Skip 有意跳过（不是错，也不是成功）
//	Warn 能用但不对劲
const (
	OK   = "✓"
	Bad  = "✗"
	Skip = "·"
	Warn = "⚠"
)

// Mark 给标记上色，调用点只管传常量。
func Mark(m string) string {
	switch m {
	case OK:
		return Green(m)
	case Bad:
		return Red(m)
	case Warn:
		return Yellow(m)
	}
	return Dim(m)
}

// ---------- 显示宽度 ----------

// Width 字符串在终端里占几列。
//
// 不能用 len() 也不能用 utf8.RuneCountInString：中文/日文/emoji 是**双宽**
// 字符。表格对齐全靠它。
func Width(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runeWidth 单字符宽度。基于 Unicode East Asian Width 的实用子集——
// 覆盖我们真的会打出来的东西（CJK、全角标点、常用 emoji），不做全表。
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || (r >= 0x7f && r < 0xa0):
		return 0 // 控制字符（含剥不干净的 ESC 残渣）
	case r >= 0x0300 && r <= 0x036f, r >= 0x200b && r <= 0x200f:
		return 0 // 组合记号与零宽字符
	case r >= 0x1100 && (r <= 0x115f || // 谚文字母
		r == 0x2329 || r == 0x232a || // 〈 〉
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || // CJK 部首 … 彝文
		(r >= 0xac00 && r <= 0xd7a3) || // 谚文音节
		(r >= 0xf900 && r <= 0xfaff) || // CJK 兼容
		(r >= 0xfe30 && r <= 0xfe6f) || // CJK 兼容标点
		(r >= 0xff00 && r <= 0xff60) || // 全角形式
		(r >= 0xffe0 && r <= 0xffe6) || // 全角符号
		(r >= 0x1f300 && r <= 0x1f5ff) || // 杂项符号与象形
		(r >= 0x1f600 && r <= 0x1f64f) || // 表情
		(r >= 0x1f680 && r <= 0x1f6ff) || // 交通与地图
		(r >= 0x1f7e0 && r <= 0x1f7eb) || // 彩色圆/方（🟢🔴）
		(r >= 0x1f90c && r <= 0x1f9ff) || // 补充符号与象形
		(r >= 0x1fa70 && r <= 0x1faff) || // 扩展 A（含大量新 emoji）
		(r >= 0x20000 && r <= 0x3fffd)): // CJK 扩展
		return 2
	}
	return 1
}

// stripANSI 去掉 SGR 序列——量宽度前必须先剥，否则颜色码会被算成字符。
func stripANSI(s string) string {
	if !strings.Contains(s, "\033") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// VisibleWidth 剥掉颜色后的显示宽度。
func VisibleWidth(s string) int { return Width(stripANSI(s)) }

// Pad 把 s 补齐到 w 列（超出则原样返回，绝不截断内容）。
func Pad(s string, w int) string {
	if n := w - VisibleWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// PadLeft 右对齐补齐。
func PadLeft(s string, w int) string {
	if n := w - VisibleWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// Truncate 按显示宽度截断并加省略号。表格里给"可能很长"的列用。
func Truncate(s string, w int) string {
	if VisibleWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	var out strings.Builder
	cur := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			if j < len(s) {
				j++
			}
			out.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if cur+rw > w-1 {
			break
		}
		out.WriteRune(r)
		cur += rw
		i += size
	}
	out.WriteRune('…')
	if strings.Contains(s, "\033") {
		out.WriteString(cReset)
	}
	return out.String()
}

// Wrap 按终端显示宽度硬换行，不丢字符。ANSI SGR 序列不计宽度；跨行时
// 临时 reset，再在下一行恢复颜色，避免颜色污染缩进或后续输出。
func Wrap(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	var lines []string
	var out strings.Builder
	active := ""
	cur := 0
	flush := func() {
		if active != "" {
			out.WriteString(cReset)
		}
		lines = append(lines, out.String())
		out.Reset()
		if active != "" {
			out.WriteString(active)
		}
		cur = 0
	}
	for i := 0; i < len(s); {
		if s[i] == '\n' {
			flush()
			i++
			continue
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			if j < len(s) {
				j++
			}
			seq := s[i:j]
			out.WriteString(seq)
			if seq == cReset {
				active = ""
			} else if strings.HasSuffix(seq, "m") {
				active += seq
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if cur > 0 && cur+rw > w {
			flush()
		}
		out.WriteRune(r)
		cur += rw
		i += size
	}
	if active != "" {
		out.WriteString(cReset)
	}
	lines = append(lines, out.String())
	return lines
}