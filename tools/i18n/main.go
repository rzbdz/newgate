// Command i18n 是本地化这件事的工具：扫源码生成账本、静态检查、看覆盖率、
// 以及**在本地**把缺的译文补上（调自己的网关跑 LLM）。
//
// 子命令分两类，别混：
//
//	不出网、不花钱（CI 里跑）      extract / check / status / missing
//	要调 LLM（**只在开发者本地跑**）  sync
//
// 用户定的规矩：翻译不进任何自动化流程。CI 只跑前一类——账本是否过期、占位符是否
// 对得上、有没有孤儿键、源码里还剩多少中文。要花钱的那一步永远是人主动敲的。
//
// 发行版仓库里有一个同名的壳（tools/i18n/main.go），它复用本目录下的 scan/check
// 两个包（通过 replace 拿到内核）——两个仓库的判断必须是同一把尺子。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/check"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "extract":
		err = cmdExtract(args)
	case "check":
		err = cmdCheck(args)
	case "status":
		err = cmdStatus(args)
	case "audit":
		err = cmdAudit(args)
	case "missing":
		err = cmdMissing(args)
	case "sync":
		err = cmdSync(args)
	case "translate":
		err = cmdTranslate(args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("不认识的子命令 %q（见 `i18n help`）", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "i18n:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`用法：go run ./tools/i18n <子命令> [选项]

  extract [-check]      扫源码重建账本（-check 只校验是否过期，不写文件）
  check [-strict]       静态检查：账本 / 占位符 / 孤儿键 / 覆盖率 / 中文残留
  status                每种语言的覆盖率与机翻计数
  audit [-seed]         看/写「还没迁移的文件」清单（-seed 按现状播种）
  missing [-lang zh-Hans]  列出缺哪些译文
  sync [-lang zh-Hans]  把缺的译文补齐（调本机网关跑 LLM，**开发者本地跑**）
  translate             同 sync，别名
`)
}

// ---------- 公共选项 ----------

type common struct {
	root       string
	catalogDir string
	allowlist  string
}

func commonFlags(fs *flag.FlagSet) *common {
	c := &common{}
	fs.StringVar(&c.root, "root", "", "仓库根（默认向上找 go.mod）")
	fs.StringVar(&c.catalogDir, "catalogs", "lib/i18n/catalogs", "目录文件所在的目录（相对仓库根）")
	fs.StringVar(&c.allowlist, "allowlist", "tools/i18n/allowlist.json", "未迁移清单（相对仓库根，空串=不查）")
	return c
}

func (c *common) resolve() error {
	if c.root != "" {
		return nil
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	c.root = root
	return nil
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("向上找不到 go.mod（从 %s 开始）", dir)
		}
		dir = parent
	}
}

// ---------- extract ----------

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	c := commonFlags(fs)
	onlyCheck := fs.Bool("check", false, "只校验账本是否过期，不写文件")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}

	fresh, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	raw, err := i18n.MarshalLedger(fresh)
	if err != nil {
		return err
	}
	path := filepath.Join(c.root, c.catalogDir, i18n.LedgerName)
	if *onlyCheck {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != string(raw) {
			return fmt.Errorf("%s 已过期：运行 `go run ./tools/i18n extract` 后重新提交", path)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("i18n: 账本 %d 条 → %s\n", len(fresh.Messages), path)
	return nil
}

// ---------- check ----------

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	c := commonFlags(fs)
	strict := fs.Bool("strict", false, "覆盖率不足与机翻未复核也算失败（发版前用）")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}

	findings, err := check.Run(check.Options{
		Root: c.root, CatalogDir: c.catalogDir, Allowlist: c.allowlist, Strict: *strict,
	})
	if err != nil {
		return err
	}
	errs, warns := 0, 0
	for _, f := range findings {
		fmt.Println(f.String())
		if f.Level == "error" {
			errs++
		} else {
			warns++
		}
	}
	if errs > 0 {
		return fmt.Errorf("%d 条错误、%d 条警告", errs, warns)
	}
	if warns > 0 {
		fmt.Printf("i18n: 0 条错误、%d 条警告\n", warns)
		return nil
	}
	fmt.Println("i18n: 全部检查通过")
	return nil
}

// ---------- status ----------

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	c := commonFlags(fs)
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	fresh, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	cats, err := check.LoadCatalogs(filepath.Join(c.root, c.catalogDir))
	if err != nil {
		return err
	}
	total := len(fresh.Messages)
	fmt.Printf("%-12s %6s %6s %8s %8s\n", "LANGUAGE", "TOTAL", "DONE", "MACHINE", "MISSING")
	rows := []string{i18n.SourceLang}
	for _, cat := range cats {
		rows = append(rows, cat.Language)
	}
	sort.Strings(rows)
	for _, lang := range rows {
		done, machine := 0, 0
		var messages map[string]i18n.Entry
		for _, cat := range cats {
			if cat.Language == lang {
				messages = cat.Messages
			}
		}
		if lang == i18n.SourceLang {
			done = total
		} else {
			for msg, e := range messages {
				if _, known := fresh.Messages[msg]; !known || e.Empty() {
					continue
				}
				done++
				if e.Machine && !e.Reviewed {
					machine++
				}
			}
		}
		fmt.Printf("%-12s %6d %6d %8d %8d\n", lang, total, done, machine, total-done)
	}
	return nil
}

// ---------- missing ----------

func cmdMissing(args []string) error {
	fs := flag.NewFlagSet("missing", flag.ExitOnError)
	c := commonFlags(fs)
	lang := fs.String("lang", "zh-Hans", "看哪门语言")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	fresh, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	cats, err := check.LoadCatalogs(filepath.Join(c.root, c.catalogDir))
	if err != nil {
		return err
	}
	have := map[string]i18n.Entry{}
	for _, cat := range cats {
		if cat.Language == *lang {
			have = cat.Messages
		}
	}
	n := 0
	for _, msg := range i18n.SortedKeys(fresh.Messages) {
		if e, ok := have[msg]; ok && !e.Empty() {
			continue
		}
		fmt.Println(msg)
		n++
	}
	fmt.Fprintf(os.Stderr, "i18n: %s 缺 %d 条\n", *lang, n)
	return nil
}

// catalogPath 是某门语言的译文文件。
func catalogPath(root, catalogDir, lang string) string {
	return filepath.Join(root, catalogDir, lang+".json")
}

// loadOne 读一门语言的译文（不存在时给一份带 meta 的空表）。
func loadOne(root, catalogDir, lang string) (i18n.Catalog, error) {
	raw, err := os.ReadFile(catalogPath(root, catalogDir, lang))
	if err != nil {
		if !os.IsNotExist(err) {
			return i18n.Catalog{}, err
		}
		return i18n.Catalog{
			Language: lang,
			Source:   i18n.SourceLang,
			Widths:   map[string]int{"label": 8, "term": 12},
			Messages: map[string]i18n.Entry{},
		}, nil
	}
	return i18n.ParseCatalog(raw)
}

func writeCatalog(root, catalogDir string, cat i18n.Catalog) error {
	raw, err := i18n.MarshalCatalog(cat)
	if err != nil {
		return err
	}
	path := catalogPath(root, catalogDir, cat.Language)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 先写临时文件再改名：半个文件写下去，下一次 parse 就失败，而失败现场
	// 是一份几千行的 JSON——比一次 rename 贵得多。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
