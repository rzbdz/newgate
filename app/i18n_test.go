package app

import (
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/tools/i18n/check"
)

// TestLocalizationStaysConsistent 是 `tools/i18n check` 的**双保险**。
//
// 判据只有一份实现（tools/i18n/check）：CI 里另有一个 step 跑同一条命令，
// 那条挡的是「忘了跑」；这条挡的是「跑过了但没人看输出」——它让本地
// `go test ./...` 就把「账本过期 / 占位符对不上 / 孤儿译文 / 源码里又冒出一个
// 中文字面量」这些问题提出来。同 app/modules_gen_test.go 对 genmodules 的做法。
//
// 2026-09-20 加进 i18n 时定的规矩：**非测试 Go 源码里的用户可见文本一律走
// i18n.T("English", …)**，中文只活在译文表里。这条棘轮就是那句话的执行者。
func TestLocalizationStaysConsistent(t *testing.T) {
	root := filepath.Join(repoDir(t), "..")
	findings, err := check.Run(check.Options{
		Root:       root,
		CatalogDir: "lib/i18n/catalogs",
		Allowlist:  "tools/i18n/allowlist.json",
	})
	if err != nil {
		t.Fatalf("本地化检查跑不起来: %v", err)
	}
	for _, f := range findings {
		if f.Level == "error" {
			t.Errorf("%s", f.String())
		}
	}

	// 退化守卫：判据自己坏掉时必须红，而不是「一条都不报」看起来像通过。
	// 这条棘轮的价值全在「它会说话」，一个哑掉的检查比没有检查更坏。
	cats, cerr := check.LoadCatalogs(filepath.Join(root, "lib/i18n/catalogs"))
	if cerr != nil {
		t.Fatalf("读不了译文目录——判据退化了: %v", cerr)
	}
	if len(cats) == 0 {
		t.Fatal("一份译文都没有——判据退化了（目录挪了？catalogs 里只剩账本？）")
	}
	led, lerr := check.LoadLedger(filepath.Join(root, "lib/i18n/catalogs"))
	if lerr != nil {
		t.Fatalf("账本读不出来——判据退化了: %v", lerr)
	}
	if len(led.Messages) == 0 {
		t.Fatal("账本是空的——扫描器坏了，或者源码里一条 i18n.T 都没有了")
	}
}
