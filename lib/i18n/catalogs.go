package i18n

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// 嵌的是 **bundle（.bin）**，不是 JSON：JSON 是给人改的，编译期那份是给运行期
// 读的。每个进程都要读一遍这些表，而解析 250KB JSON 实测 2.78ms——占一次
// `newgate …` 装配（约 6ms）的将近一半。两种形态由 `tools/i18n bundle` 对齐，
// `-check` 与 lib/i18n 的测试拦「改了 JSON 忘了重生成」。
//
//go:embed catalogs/*.bin
var catalogsFS embed.FS

// LedgerName 是账本的文件名。它**不是**一门语言，是源语言的清单，
// 所以不能按 `<tag>.json` 的规矩来（否则会被当成一门叫 ledger 的语言）。
const LedgerName = "ledger.json"

// Builtin 读内核自带的账本与译文（编译进二进制的那一份）。
//
// 为什么内置：`newgate` 是**单文件静态二进制**，要能拷到别的机器上直接跑，不能
// 依赖旁边躺着一个 locale/ 目录。磁盘那一层（OverlayDir）是**覆盖**，不是唯一来源。
func Builtin() (Ledger, []Catalog, error) {
	led, err := ledgerFromBundle(catalogsFS, "catalogs")
	if err != nil {
		return Ledger{}, nil, err
	}
	cats, err := BundlesFromFS(catalogsFS, "catalogs")
	if err != nil {
		return Ledger{}, nil, err
	}
	return led, cats, nil
}

// BundlesFromFS 从一个目录里读全部 `<tag>.bin`（编译期生成的那一份，见 bundle.go）。
//
// 与 CatalogsFromFS 是同一件事的两种输入：那边读 JSON（人写的、工具写的、磁盘覆盖
// 用的），这边读 bundle（运行期用的）。**必须是两份实现而不是「自动挑一种」**：
// 自动挑的那条会在「目录里两种都有」时给出一个看运气的结果，而那种不确定正是
// 「改了 JSON 忘了重生成」最难查的形态。
func BundlesFromFS(fsys fs.FS, dir string) ([]Catalog, error) {
	names, err := bundleNames(fsys, dir)
	if err != nil {
		return nil, err
	}
	out := make([]Catalog, 0, len(names))
	for _, name := range names {
		if name == bundleLedgerName {
			continue
		}
		raw, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		c, err := DecodeCatalog(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, name, err)
		}
		// 文件名与 meta.language 必须一致（理由同 CatalogsFromFS 里那一条）。
		if want := strings.TrimSuffix(name, ".bin"); want != c.Language {
			return nil, fmt.Errorf("%s/%s: language is %q, which disagrees with the file name",
				dir, name, c.Language)
		}
		out = append(out, c)
	}
	return out, nil
}

// LedgerBundleName 是账本的 bundle 文件名（LedgerName 的编译期形态）。
const LedgerBundleName = "ledger.bin"

const bundleLedgerName = LedgerBundleName

func ledgerFromBundle(fsys fs.FS, dir string) (Ledger, error) {
	raw, err := fs.ReadFile(fsys, dir+"/"+bundleLedgerName)
	if err != nil {
		return Ledger{}, fmt.Errorf("cannot read the ledger bundle %s/%s: %w", dir, bundleLedgerName, err)
	}
	led, err := DecodeLedger(raw)
	if err != nil {
		return Ledger{}, fmt.Errorf("%s/%s: %w", dir, bundleLedgerName, err)
	}
	return led, nil
}

func bundleNames(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bin") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
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

// catalogsJSONForTest 让测试能读**磁盘上**那份 JSON（它已经不在 embed 里了：嵌的
// 是 bundle）。只给测试用——运行期不许走 JSON，那正是这次要消掉的那笔开销。
func catalogsJSONForTest() fs.FS { return os.DirFS(".") }
