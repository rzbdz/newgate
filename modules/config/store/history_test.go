package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 写盘之前留一份「上一版」。
//
// 这几条锁的是**一次改坏之后还能翻回去**：2026-09-20 现场是一个前端 bug 把用户
// 某个 profile 的档位写没了，而当时没有任何地方能找回写之前那一份（`backups/`
// 是接管用的、配置目录不在 git 里）。判据是代价不对称——留备份是几 KB，丢一次
// 是用户手上的配置。

// TestAWriteKeepsThePreviousVersion：写一次之后，**上一版**能从历史里读回来。
func TestAWriteKeepsThePreviousVersion(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "x.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before"), 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteIfUnchanged(path, Revision(path), []byte("after")); err != nil {
		t.Fatal(err)
	}

	// 盘上是新的。
	b, _ := os.ReadFile(path)
	if string(b) != "after" {
		t.Fatalf("写入没生效: %q", b)
	}
	// 历史里是旧的。
	hist := HistoryEntries(path)
	if len(hist) != 1 {
		t.Fatalf("该留一版历史，实际 %d 版", len(hist))
	}
	old, err := os.ReadFile(hist[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != "before" {
		t.Errorf("历史里该是写之前那份，实际 %q", old)
	}
}

// TestTheHistoryIsARing：只留最近 20 版，不能无限长。
//
// 配置目录会被反复写（界面上每点一次保存就是一次），留全量等于把配置目录撑成
// 一个只涨不消的日志。
func TestTheHistoryIsARing(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("v0"), 0o660); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= historyKeep+5; i++ {
		if _, err := WriteIfUnchanged(path, Revision(path), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	hist := HistoryEntries(path)
	if len(hist) != historyKeep {
		t.Fatalf("该只留 %d 版，实际 %d 版", historyKeep, len(hist))
	}
	// 留下的必须是**最近**的那 20 版：最旧的那一版里是 v5（v0..v4 已被挤掉），
	// 最新那一版里是 v24（= 倒数第二次写之前的内容）。
	oldest, err := os.ReadFile(hist[len(hist)-1])
	if err != nil {
		t.Fatal(err)
	}
	if string(oldest) != fmt.Sprintf("v%d", 5) {
		t.Errorf("挤掉的该是最旧的几版，实际最旧一版是 %q", oldest)
	}
	newest, err := os.ReadFile(hist[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(newest) != fmt.Sprintf("v%d", historyKeep+4) {
		t.Errorf("最新一版该是最后一次写之前的内容，实际 %q", newest)
	}
}

// TestCreatingAFileLeavesNoHistory：新建（盘上本来没有）没什么可留的——留一个空
// 版本只会让「翻历史」的人以为那一刻文件是空的。
func TestCreatingAFileLeavesNoHistory(t *testing.T) {
	dir := sandbox(t)
	path := filepath.Join(dir, "mappings", "fresh.kv")
	if _, err := WriteIfUnchanged(path, "", []byte("desc=new\n")); err != nil {
		t.Fatal(err)
	}
	if hist := HistoryEntries(path); len(hist) != 0 {
		t.Errorf("新建一份文件不该留下历史，实际 %d 版", len(hist))
	}
}

// TestHistoryIsPerFile：历史按文件分家。共用一锅的话，回滚某一份时会看到别人的
// 内容，而那比没有备份更危险。
func TestHistoryIsPerFile(t *testing.T) {
	dir := sandbox(t)
	a := filepath.Join(dir, "mappings", "a.kv")
	b := filepath.Join(dir, "mappings", "b.kv")
	if err := os.MkdirAll(filepath.Dir(a), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("old-"+filepath.Base(p)), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteIfUnchanged(a, Revision(a), []byte("new-a")); err != nil {
		t.Fatal(err)
	}
	if hist := HistoryEntries(b); len(hist) != 0 {
		t.Errorf("没写过的文件不该有历史，实际 %d 版", len(hist))
	}
	hist := HistoryEntries(a)
	if len(hist) != 1 {
		t.Fatalf("写过的那个该有一版，实际 %d", len(hist))
	}
	got, _ := os.ReadFile(hist[0])
	if string(got) != "old-a.kv" {
		t.Errorf("历史串了别人的内容: %q", got)
	}
}
