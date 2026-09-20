package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ---------- bundle ----------
//
// 把 catalogs/*.json 编成运行期直接读的二进制（见 lib/i18n/bundle.go 的格式注释）。
//
// 它是**派生**步骤：JSON 是唯一的真相，人改 JSON、工具读写 JSON，这一份随时可以
// 删掉重生成。之所以还把它提交进版本控制，是为了让 `-check` 能拦住「改了 JSON 忘
// 了重生成」——同 manifest/modules_gen.go 那条路（生成物进库 + CI 校验）。
func cmdBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	c := commonFlags(fs)
	onlyCheck := fs.Bool("check", false, "只校验 bundle 是否过期，不写文件")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	dir := filepath.Join(c.root, c.catalogDir)
	names, err := jsonFileNames(dir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("%s 里一个 .json 都没有", dir)
	}

	stale := []string{}
	for _, name := range names {
		src := filepath.Join(dir, name)
		raw, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		var out []byte
		if name == i18n.LedgerName {
			led, err := i18n.ParseLedger(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
			out = i18n.EncodeLedger(led)
		} else {
			cat, err := i18n.ParseCatalog(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
			// 文件名与 meta.language 必须一致——CatalogsFromFS 里也有一条同样的
			// 检查，理由在那（改的是文件 A、生效的是文件 B 会变成一次无解的排查）。
			if want := strings.TrimSuffix(name, ".json"); want != cat.Language {
				return fmt.Errorf("%s: meta.language 是 %q，与文件名对不上", src, cat.Language)
			}
			out = i18n.EncodeCatalog(cat)
		}
		dst := filepath.Join(dir, strings.TrimSuffix(name, ".json")+".bin")
		if *onlyCheck {
			got, rerr := os.ReadFile(dst)
			if rerr != nil || string(got) != string(out) {
				stale = append(stale, filepath.Base(dst))
			}
			continue
		}
		if err := os.WriteFile(dst, out, 0o644); err != nil {
			return err
		}
		fmt.Printf("i18n: %s → %s（%d 字节）\n", name, filepath.Base(dst), len(out))
	}
	if *onlyCheck {
		if len(stale) > 0 {
			return fmt.Errorf("%s 已过期：运行 `go run ./tools/i18n bundle` 后重新提交",
				strings.Join(stale, ", "))
		}
		fmt.Printf("i18n: %d 份 bundle 都是新的\n", len(names))
	}
	return nil
}

// jsonFileNames 列出目录里的 .json（排序：目录序与文件系统有关，排序保证结果稳定）。
func jsonFileNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
