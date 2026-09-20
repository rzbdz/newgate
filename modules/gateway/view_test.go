package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 日志那条概念的取材部分（tailLines / redactLine）是纯函数，所以它在叶子里就能
// 测：起一个真文件，读它的尾巴。这条路径的错误都不吵——读多了、读少了、把凭据
// 发出去了，界面上看起来都只是「日志」。所以它值得几条断言。

func writeLog(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "newgate.log")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTailLinesReturnsTheLastOnesOldestFirst(t *testing.T) {
	p := writeLog(t, "a\nb\nc\nd\ne\n")
	// 从旧到新：最新的一行在最下面（见 logData.Lines 的说明）。
	lines, truncated, err := tailLines(p, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "a|b|c|d|e" {
		t.Errorf("整个文件都读得下，该原样给出: %q", got)
	}
	if truncated {
		t.Error("一条都没丢，不该说 truncated")
	}

	// 只要最后 3 行：上面那两行**确实**没给出去，所以 truncated 该是 true——
	// 「你看的不是全部」这件事不说是会骗人的（读的人会以为日志从这里开始）。
	lines, truncated, err = tailLines(p, 3, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "c|d|e" {
		t.Errorf("尾巴取错了: %q", got)
	}
	if !truncated {
		t.Error("上面还有两行没给出来，该说 truncated")
	}
}

// TestTailLinesStopsAtTheByteLimit：日志按 16MB×4 轮转，而浏览器每隔几秒就刷新
// 一次这个视图。所以读取必须是**有上界的**——按行回溯会把整个文件读一遍，那个
// 成本随日志增长，而界面看起来只是「有点慢」。
func TestTailLinesStopsAtTheByteLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString(strings.Repeat("x", 100))
		b.WriteString("\n")
	}
	b.WriteString("last line\n")
	p := writeLog(t, b.String())

	lines, truncated, err := tailLines(p, 400, 512)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("被字节上限切过，该说 truncated——不说的话用户以为日志只有这么长")
	}
	if len(lines) == 0 || lines[len(lines)-1] != "last line" {
		t.Errorf("尾巴的最后一行该是最新的那条，实际 %q", lines[len(lines)-1:])
	}
	// 第一行多半是半截的（被 limit 切开），必须丢掉——留下它，读的人会拿一段
	// 没有开头的输出当证据。
	if strings.Contains(lines[0], "x") && len(lines[0]) != 100 {
		t.Errorf("第一行是半截的: %q", lines[0])
	}
}

func TestTailLinesOnMissingFile(t *testing.T) {
	_, _, err := tailLines(filepath.Join(t.TempDir(), "nope.log"), 10, 1024)
	if err == nil {
		t.Error("文件不存在该报错——调用方据此说「还没有日志」而不是「日志是空的」")
	}
}

// TestRedactLine：日志本来不该出现凭据，但它是**别人写进来的**（provider 的 URL、
// 某个模块打出来的请求头、将来某个插件记的 body），而这个视图会把日志发到浏览器。
// 兜底判据宁可误杀。
func TestRedactLine(t *testing.T) {
	cases := map[string]string{
		"using key sk-abcdefghijklmnopqrst for demo": "using key *** for demo",
		`provider api_key=abcdefghijklmnop sent`:     `provider api_key=*** sent`,
		`Authorization: Bearer abcdefghijklmnop.qrs`: `Authorization: ***`,
		"-> deepseek/model-heavy (200)":              "-> deepseek/model-heavy (200)",
	}
	for in, want := range cases {
		if got := redactLine(in); got != want {
			t.Errorf("redactLine(%q) = %q，想要 %q", in, got, want)
		}
	}
}
