package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDirStopsAtNestedModule locks down "the scanner does not enter someone else's tree".
//
// Why this matters: the distribution is a separate module, and its `core/` is the kernel
// submodule (carrying its own go.mod). If the scanner walked in, the distribution's ledger
// would suddenly sprout a pile of the kernel's messages -- the symptom is the coverage
// denominator growing for no reason and the allowlist listing `core/...` files (observed once),
// while the ledger itself looks perfectly normal. One tree, two rulers.
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

	// The same rule must govern the Chinese count too -- the two criteria must measure the same
	// tree.
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
