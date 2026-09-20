package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/rzbdz/newgate/tools/i18n/check"
)

// cmdAudit 看/写「还没迁移的文件」清单。
//
// 迁移开始时先 `-seed` 播一次种（把今天所有含中文字面量的文件登记进去），
// 此后每迁完一个文件就从清单里删一行——清单只减不增，终点是空文件。
func cmdAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	c := commonFlags(fs)
	seed := fs.Bool("seed", false, "按现状重写未迁移清单（迁移起点用）")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	seen, err := check.CJKFiles(c.root)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	if !*seed {
		if len(files) == 0 {
			fmt.Println("i18n: 非测试源码里已经没有中文字面量了")
			return nil
		}
		fmt.Printf("i18n: %d 个文件里还有中文字面量\n", len(files))
		for _, f := range files {
			fmt.Printf("  %-60s %d 处\n", f, seen[f])
		}
		return nil
	}

	// 播种时保留已有的理由（人写的那句话不能被一次重播抹掉）。
	existing := map[string]string{}
	if raw, rerr := os.ReadFile(filepath.Join(c.root, c.allowlist)); rerr == nil {
		var prev struct {
			Files map[string]string `json:"files"`
		}
		if json.Unmarshal(raw, &prev) == nil {
			existing = prev.Files
		}
	}
	out := struct {
		Why   string            `json:"why"`
		Files map[string]string `json:"files"`
	}{
		Why: "豁免清单：「非测试 Go 源码里不许有用户可见的中文字面量」这条判据的例外。" +
			"判据护的是**用户可见文本**，所以豁免只有三类，每一类都要写下自己的理由：" +
			"(1) 翻译层自己——它要在目录装好之前、装坏之后说话；(2) 开发工具——读者是开发者，" +
			"不是 CLI 用户；(3) 测试夹具——读者是跑测试的人，而且 CI 日志要稳定。" +
			"产品源码不该出现在这里：**清单只减不增**，新条目一律先问「为什么它不是 i18n.T」。" +
			"迁移期剩下的「还没迁」条目会被 `audit --seed` 收缩掉。",
		Files: map[string]string{},
	}
	for _, f := range files {
		reason := existing[f]
		if reason == "" {
			reason = "还没迁（迁移期豁免）"
		}
		out.Files[f] = reason
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(filepath.Join(c.root, c.allowlist)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.root, c.allowlist), raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("i18n: 未迁移清单写了 %d 个文件 → %s\n", len(files), c.allowlist)
	return nil
}
