// Package check 是本地化这件事的**静态检查**：账本是否过期、译文与原文的占位符
// 是否对得上、有没有孤儿键、有没有把机器标记当成文案、源码里还剩下多少中文。
//
// 它与 tools/i18n/scan 一样，是内核与发行版**共用**的一份实现（发行版通过
// replace 拿到它）：两个仓库的规矩必须是同一把尺子，否则「发行版里能过、内核里
// 报错」这种事会变成日常。
//
// 全部检查都是**离线、不花钱**的——CI 里跑的就是它。要调 LLM 的那一步（翻译）
// 不在这里，见 tools/i18n/main.go 的 sync 子命令：那是开发者本地跑的事。
package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/scan"
)

// Options 是一次检查的输入。两个仓库各给各的路径。
type Options struct {
	Root       string // 仓库根
	CatalogDir string // 目录文件相对 Root 的位置，如 "lib/i18n/catalogs"
	Allowlist  string // 未迁移文件清单（相对 Root）；空 = 跳过这项检查
	Strict     bool   // 覆盖率不足、机翻未复核是否算失败（发版时用）
}

// Finding 是一条检查结论。Level 为 "error" 时调用方应当以非零退出。
type Finding struct {
	Level string // error | warn
	Where string
	Msg   string
}

func (f Finding) String() string {
	where := f.Where
	if where == "" {
		where = "-"
	}
	return fmt.Sprintf("%-5s %s: %s", f.Level, where, f.Msg)
}

// Run 跑完全部检查。
func Run(opts Options) ([]Finding, error) {
	var out []Finding
	add := func(level, where, msg string) { out = append(out, Finding{level, where, msg}) }

	calls, err := scan.Dir(opts.Root)
	if err != nil {
		return nil, err
	}
	for _, p := range scan.Problems(calls) {
		add("error", strings.SplitN(p, ":", 2)[0], p)
	}
	msgs := scan.Messages(calls)

	ledgerPath := filepath.Join(opts.Root, opts.CatalogDir, i18n.LedgerName)
	wantLedger := buildLedger(msgs)
	wantRaw, err := i18n.MarshalLedger(wantLedger)
	if err != nil {
		return nil, err
	}
	gotRaw, err := os.ReadFile(ledgerPath)
	if err != nil {
		add("error", ledgerPath, "读不到账本："+err.Error())
	} else if !bytes.Equal(gotRaw, wantRaw) {
		add("error", ledgerPath, "账本已过期：源码里的消息与它对不上。"+
			"运行 `go run ./tools/i18n extract` 后重新提交")
	}

	// 调用点给的实参必须盖住消息里的占位符。
	//
	// 为什么值得单独一条：漏一个实参**不报错**，界面上就永远显示一个光秃秃的
	// `{dir}`——只有用户会看见，而用户不会为此开 issue（他以为那是我们想显示的
	// 东西）。这是 i18n 里唯一「静默到没人会来修」的失效，所以放在构建期拦。
	//
	// 两个**注入**例外（写在 lib/i18n 里）：N() 自己把数量放进实参、Ef() 自己把
	// 底层错误放进 {err}——这两处调用点可以不写。除此之外一个都不能少。
	for _, msg := range i18n.SortedKeys(msgs) {
		call := msgs[msg]
		want := i18n.Placeholders(msg)
		if call.Plural != "" {
			want = union(want, i18n.Placeholders(call.Plural))
		}
		have := map[string]bool{}
		for _, a := range call.Args {
			have[a] = true
		}
		if call.Plural != "" {
			have["n"] = true // N() 注入
		}
		if call.Method == "Ef" {
			have["err"] = true // Ef() 注入
		}
		var missing []string
		for _, p := range want {
			if !have[p] {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			add("error", call.Where, fmt.Sprintf(
				"%q 里的占位符 %v 在调用点没有对应实参——界面上会原样显示 {%s}。"+
					"（N() 注入 n、Ef() 注入 err，其余都要写进 i18n.A{…}）",
				msg, missing, missing[0]))
		}
	}

	// 译文：占位符一致、没有孤儿键、覆盖率。
	catalogs, err := loadCatalogs(filepath.Join(opts.Root, opts.CatalogDir))
	if err != nil {
		return nil, err
	}
	total := len(msgs)
	for _, cat := range catalogs {
		if cat.Language == i18n.SourceLang {
			add("error", cat.Language, "源语言不该有译文文件——它的文本就是源码里那句")
			continue
		}
		translated := 0
		for _, msg := range i18n.SortedKeys(cat.Messages) {
			e := cat.Messages[msg]
			call, known := msgs[msg]
			if !known {
				add("error", cat.Language, fmt.Sprintf(
					"孤儿译文 %q：源码里已经没有这条消息了（改了英文措辞？）——删掉它，或改回去", msg))
				continue
			}
			if e.Empty() {
				continue
			}
			translated++
			if e.Machine && !e.Reviewed && opts.Strict {
				add("error", cat.Language, fmt.Sprintf("机翻未复核：%q", msg))
			}
			want := i18n.Placeholders(msg)
			if call.Plural != "" {
				want = union(want, i18n.Placeholders(call.Plural))
			}
			got := i18n.EntryPlaceholders(e)
			if strings.Join(want, ",") != strings.Join(got, ",") {
				add("error", cat.Language, fmt.Sprintf(
					"占位符对不上：%q\n      原文要 %v，译文给了 %v（漏一个占位符，界面上就会永远显示 {name}）",
					msg, want, got))
			}
		}
		missing := total - translated
		if missing > 0 {
			level := "warn"
			if opts.Strict {
				level = "error"
			}
			add(level, cat.Language, fmt.Sprintf("缺 %d 条译文（共 %d 条）", missing, total))
		}
	}

	// 机器标记不许被当成文案：它们是控制流的判据、协议里的名字。
	for _, cat := range catalogs {
		for _, msg := range i18n.SortedKeys(cat.Messages) {
			if forbidden[msg] {
				add("error", cat.Language, fmt.Sprintf(
					"%q 是机器标记（控制流判据/协议名），不该出现在目录里", msg))
			}
		}
	}

	if opts.Allowlist != "" {
		findings, err := auditCJK(opts.Root, filepath.Join(opts.Root, opts.Allowlist))
		if err != nil {
			return nil, err
		}
		out = append(out, findings...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out, nil
}

// forbidden 是**绝不许**进目录的字符串——它们看着像文案，其实是机器读的。
//
// 结构上它们根本不经过 i18n.T（`Diagnostic.State` 是 switch 的判据、metrics 名是
// 协议字段），所以这条检查防的是**译者手滑**：有人把 `ok` 做成一条消息改成
// 「正常」，那一天 `newgate doctor` 的判据就断了。
var forbidden = map[string]bool{
	"ok": true, "warn": true, "bad": true, "skip": true,
	"providers.json": true, "state.json": true, "health.json": true,
	"/__newgate/status": true, "/__newgate/metrics": true,
}

// buildLedger 把扫描结果变成账本。人写的 note 保留（extract 绝不覆盖人写的东西）。
//
// **不记位置**（2026-09-21）：`scan.Call` 有 Where（`文件:行`，报错时点名用），但账本
// 不留它——位置一变账本就"过期"，而那是每次挪一行代码都会发生的事。理由与代价写在
// lib/i18n 的 Ledger 注释里。
func buildLedger(msgs map[string]scan.Call) i18n.Ledger {
	led := i18n.Ledger{Messages: make(map[string]i18n.LedgerEntry, len(msgs))}
	for msg, call := range msgs {
		led.Messages[msg] = i18n.LedgerEntry{
			Args:  call.Args,
			Other: call.Plural,
		}
	}
	return led
}

// MergeLedger 把新扫出来的账本与旧账本合并：消息的 args 用新的，**note 用旧的**。
func MergeLedger(old, fresh i18n.Ledger) i18n.Ledger {
	out := i18n.Ledger{Messages: make(map[string]i18n.LedgerEntry, len(fresh.Messages))}
	for msg, e := range fresh.Messages {
		if prev, ok := old.Messages[msg]; ok {
			e.Note = prev.Note
		}
		out.Messages[msg] = e
	}
	return out
}

// FreshLedger 扫源码算出当前应有的账本（extract 与 check 共用这一份）。
func FreshLedger(root, catalogDir string) (i18n.Ledger, error) {
	calls, err := scan.Dir(root)
	if err != nil {
		return i18n.Ledger{}, err
	}
	if problems := scan.Problems(calls); len(problems) > 0 {
		return i18n.Ledger{}, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	old, _ := LoadLedger(filepath.Join(root, catalogDir))
	return MergeLedger(old, buildLedger(scan.Messages(calls))), nil
}

// LoadLedger 读一份账本；读不到给空账本（第一次跑 extract 时就是这个状态）。
func LoadLedger(path string) (i18n.Ledger, error) {
	raw, err := os.ReadFile(filepath.Join(path, i18n.LedgerName))
	if err != nil {
		return i18n.Ledger{Messages: map[string]i18n.LedgerEntry{}}, nil
	}
	return i18n.ParseLedger(raw)
}

// LoadCatalogs 读目录下全部译文（不含账本）。
func LoadCatalogs(dir string) ([]i18n.Catalog, error) { return loadCatalogs(dir) }

func loadCatalogs(dir string) ([]i18n.Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []i18n.Catalog
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == i18n.LedgerName {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		c, err := i18n.ParseCatalog(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		out = append(out, c)
	}
	return out, nil
}

// ---------- 中文残留的棘轮 ----------

// allowlistFile 是「还没迁移的文件」清单。
//
// 它是**迁移期**的脚手架，终点是空文件。为什么按文件而不是按字面量列：迁移是
// 一个文件一个文件做的（一个模块一批），按文件列才跟得上手速；代价是未迁移的
// 文件里可以偷偷加新的中文——但那份清单只减不增，终点是「非测试源码里一个字都
// 没有中文」，所以这个漏洞有明确的到期日。
type allowlistFile struct {
	Why   string            `json:"why"`
	Files map[string]string `json:"files"` // 路径 → 为什么还没迁（一句话）
}

// CJKFiles 扫出「哪些非测试源码文件里还有中文字符串字面量」，返回 文件 → 条数。
//
// 只看**字符串字面量**：注释是设计记录，本来就该是中文；测试文件里的中文是夹具。
// 两者都不该被这条检查管。导出是因为 tools/i18n 的 `audit --seed` 要用它生成
// 未迁移清单，而发行版复用同一份判据。
func CJKFiles(root string) (map[string]int, error) {
	seen := map[string]int{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 走哪些目录、不走哪些，与 scan.Dir 用**同一条规则**（scan.SkipDir）：
			// 两条判据量的必须是同一棵树，否则会出现「账本里没有它、豁免清单里却
			// 列着它」这种自相矛盾——而发行版里 `core/` 那棵 submodule 正好是这
			// 种错位最容易发生的地方。
			if scan.SkipDir(root, p) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") ||
			strings.HasSuffix(p, "_gen.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, p)
		count := 0
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, uerr := strconv.Unquote(lit.Value); uerr == nil && hasCJK(s) {
				count++
			}
			return true
		})
		if count > 0 {
			seen[filepath.ToSlash(rel)] = count
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return seen, nil
}

// auditCJK 是那条棘轮：清单与现状必须**精确相等**。
//
// 两个方向都要红。「多出来」拦的是新增的中文（该走 i18n.T）；「少下去」拦的是
// 迁完了忘了删清单——只查一个方向的话，清单会慢慢腐坏，腐坏之后它就只是一份
// 谁也不敢删的历史文件。
func auditCJK(root, path string) ([]Finding, error) {
	var out []Finding
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读不到未迁移清单 %s: %w", path, err)
	}
	var al allowlistFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&al); err != nil {
		return nil, fmt.Errorf("%s 解不出来: %w", path, err)
	}
	seen, err := CJKFiles(root)
	if err != nil {
		return nil, err
	}
	for file, n := range seen {
		if _, ok := al.Files[file]; !ok {
			out = append(out, Finding{"error", file, fmt.Sprintf(
				"这里有 %d 处中文字面量，但不在未迁移清单里——用户可见的文案请走 "+
					"i18n.T(\"English\", …)；确实是机器标记的话，说明这条检查的判据该改了", n)})
		}
	}
	for file := range al.Files {
		if _, ok := seen[file]; !ok {
			out = append(out, Finding{"error", file, "未迁移清单里列着它，但它已经没有中文字面量了——" +
				"把它从清单里删掉（清单只减不增）"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where < out[j].Where })
	return out, nil
}

func hasCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
			return true
		case r >= 0x3000 && r <= 0x303F: // 中文标点（。、：（））
			return true
		case r >= 0xFF00 && r <= 0xFFEF: // 全角标点
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, x := range b {
		set[x] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
