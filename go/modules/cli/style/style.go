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