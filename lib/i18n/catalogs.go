package i18n

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed catalogs/*.json
var catalogsFS embed.FS

// LedgerName 是账本的文件名。它**不是**一门语言，是源语言的清单，
// 所以不能按 `<tag>.json` 的规矩来（否则会被当成一门叫 ledger 的语言）。
const LedgerName = "ledger.json"

// Builtin 读内核自带的账本与译文（编译进二进制的那一份）。
//
// 为什么内置：`newgate` 是**单文件静态二进制**，要能拷到别的机器上直接跑，不能
// 依赖旁边躺着一个 locale/ 目录。磁盘那一层（OverlayDir）是**覆盖**，不是唯一来源。
func Builtin() (Ledger, []Catalog, error) {
	led, err := ledgerFromFS(catalogsFS, "catalogs")
	if err != nil {
		return Ledger{}, nil, err
	}
	cats, err := CatalogsFromFS(catalogsFS, "catalogs")
	if err != nil {
		return Ledger{}, nil, err
	}
	return led, cats, nil
}

// CatalogsFromFS 从一个目录里读全部 `<tag>.json` 译文（`ledger.json` 不算）。
//
// 发行版模块带自己的文案时也用它：发行版是独立 module，内核不认识它的目录名，
// 所以「把自己的表嵌进来并注册」这件事必须由它自己做。
func CatalogsFromFS(fsys fs.FS, dir string) ([]Catalog, error) {
	names, err := jsonNames(fsys, dir)
	if err != nil {
		return nil, err
	}
	out := make([]Catalog, 0, len(names))
	for _, name := range names {
		if name == LedgerName {
			continue
		}
		raw, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		c, err := ParseCatalog(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, name, err)
		}
		// 文件名与 meta.language 必须一致：不一致时「我改了 zh-Hans.json 怎么没
		// 生效」会变成一次无解的排查（改的是文件 A，生效的是文件 B）。
		if want := strings.TrimSuffix(name, ".json"); want != c.Language {
			return nil, fmt.Errorf("%s/%s: meta.language is %q, which disagrees with the file name",
				dir, name, c.Language)
		}
		out = append(out, c)
	}
	return out, nil
}

// ledgerFromFS 读账本。
func ledgerFromFS(fsys fs.FS, dir string) (Ledger, error) {
	raw, err := fs.ReadFile(fsys, dir+"/"+LedgerName)
	if err != nil {
		return Ledger{}, fmt.Errorf("cannot read the ledger %s/%s: %w", dir, LedgerName, err)
	}
	led, err := ParseLedger(raw)
	if err != nil {
		return Ledger{}, fmt.Errorf("%s/%s: %w", dir, LedgerName, err)
	}
	return led, nil
}

func jsonNames(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 目录序与文件系统有关，排序保证结果稳定
	return names, nil
}
