package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDirStopsAtNestedModule 锁住「扫描器不进别人家的树」。
//
// 为什么这条要紧：发行版是独立 module，它的 `core/` 是内核那份 submodule（自带
// go.mod）。扫描器若走进去，发行版的账本里会冒出一堆内核的消息——症状是覆盖率
// 分母莫名变大、豁免清单里列出 `core/...` 的文件（实测过一次），而账本本身看起来
// 完全正常。同一棵树，两把尺子。
func TestDirStopsAtNestedModule(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module ours\n")
	write("ours.go", `package ours

import i18n "github.com/rzbdz/newgate/lib/i18n"

var _ = i18n.T("a message of ours", nil)
`)
	write("core/go.mod", "module theirs\n")
	write("core/theirs.go", `package theirs

import i18n "github.com/rzbdz/newgate/lib/i18n"

var _ = i18n.T("a message of theirs", nil)
`)

	calls, err := Dir(root)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	msgs := Messages(calls)
	if _, ok := msgs["a message of ours"]; !ok {
		t.Error("自己家的消息没扫到")
	}
	if _, ok := msgs["a message of theirs"]; ok {
		t.Error("扫进了嵌套的 Go module（别人家的消息进了我们的账本）")
	}

	// 同一条规则必须也管住中文清点——两条判据量的得是同一棵树。
	if !SkipDir(root, filepath.Join(root, "core")) {
		t.Error("嵌套 module 该被跳过")
	}
	if SkipDir(root, root) {
		t.Error("根目录自己没有 go.mod 判据之外的豁免——跳过它会让扫描扫出空结果")
	}
	if !SkipDir(root, filepath.Join(root, "vendor")) {
		t.Error("vendor 该被跳过")
	}
	if got := strings.Join(Problems(calls), "\n"); got != "" {
		t.Errorf("这份夹具里没有坏调用，却报了出来:\n%s", got)
	}
}
