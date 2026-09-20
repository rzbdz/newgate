// Command genmodules 在构建期扫描 go/modules/，生成组合根要装配的组件清单。
//
// 为什么要有这一步
//
// 组合根以前手工维护一份 `config.New(), gateway.New(), …` 的列表。想装一个
// 模块就得改这行清单——而清单本身不携带任何信息，纯粹是「有哪些目录」的
// 手工副本：加目录、加 import、加调用，三处都要动，漏一处就是编译错误或者
// 更糟的「模块在仓库里但没被装配」。
//
// 所以规则改成：modules/ 下每个含根 module.go 并导出 New() 的目录都是一个
// 组件，清单由本工具扫描生成。装模块 = 把目录复制进来、重新编译。
//
// 判据刻意收得很紧——目录里有 module.go **且**它导出 `func New() modules.Component`
// 才算组件。这让我们能在 modules/ 下放纯实现子包（gateway/forward 之类），
// 而不会被误当成组件。
//
// # 第二个来源：modules-ext（外部发行版仓库）
//
// 2026-09-20 起，清单有**两个**来源：
//
//	modules/      本发行版自带（全部装上）
//	modules-ext/  外部仓库的 checkout，装哪些由仓库根的 modules-ext.json 点名
//
// 两份都进同一个清单、走同一张依赖图——对内核来说它们没有区别。区别只在
// **谁决定装**：自带的按目录扫描，外部的按声明点名（见 tools/extmanifest）。
//
// 为什么外部模块不是「用户自己写个 Loader」：那样每个用 ext 的人都要改组合根，
// 而组合根的目标恰恰是不认识任何模块。声明是一个数据文件，不需要谁来写代码。
//
// **checkout 是本工具拉下来的**（extmanifest.Prepare），不是用户手工 clone 到
// 一个约定路径。这一步刻意放在构建期：声明说「装 ext 的 hello」，而 hello 在不在
// 本机、是不是那一版，不该靠人记得做对——同一个提交 + 同一份 json 在任何机器上
// 都要装出同一个产品。代价与开关（NEWGATE_EXT_DRYRUN）写在 extmanifest 的包注释里。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate/go/tools/extmanifest"
	modscan "github.com/rzbdz/newgate/go/tools/genmodules/scan"
)

const (
	modulesRelDir = "modules"
	genFileName   = "modules_gen.go"
)

// module 是一个待装配的组件目录。
type module struct {
	dir   string // 相对 go/ 的目录（`foo` 或 `modules-ext/hello`），用于日志与查重
	path  string // 相对 go/ 的 import 路径后缀（`modules/foo` / `modules-ext/hello`）
	alias string // 生成的 import 别名，统一前缀避免与局部变量撞名
}

func main() {
	check := flag.Bool("check", false,
		"只校验生成结果是否与当前源码一致，不写文件（测试用）")
	// -ext 只把发行版的 checkout 准备好就退出，不生成清单。
	//
	// 为什么需要它：清单里若有 modules-ext 下的包，**编译就需要那份 checkout**，
	// 而 CI 的第一步（gofmt/vet）跑在 generate 之前。2026-09-20 实测：静态检查
	// 那个 job 在 `go vet ./...` 上红——生成的清单 import 了还不存在的包。
	// 与其让每个 job 记住「先跑一次 generate」，不如给一个说得清名字的目标。
	extOnly := flag.Bool("ext", false,
		"只准备发行版 checkout（按 modules-ext.json 拉取发行版代码），不写清单")
	flag.Parse()

	root, err := moduleRoot()
	if err != nil {
		fail(err)
	}
	manifest, hasManifest, err := extmanifest.Load(filepath.Dir(root))
	if *extOnly {
		if err != nil {
			fail(err)
		}
		if !hasManifest {
			fmt.Println("genmodules: 没有发行版声明（modules-ext.json），本构建不带外部模块")
			return
		}
		fmt.Printf("genmodules: 发行版 %s @ %s 已就位（%s）\n",
			manifest.Distribution(), shortRev(manifest.Resolved), extmanifest.Dir(filepath.Dir(root)))
		return
	}
	if err != nil {
		fail(err)
	}
	modules, err := scan(filepath.Join(root, modulesRelDir))
	if err != nil {
		fail(err)
	}
	if hasManifest {
		if err := extmanifest.CheckDisable(filepath.Dir(root), manifest); err != nil {
			fail(err)
		}
		kept := modules[:0]
		for _, m := range modules {
			if manifest.Disables(m.dir) {
				fmt.Printf("genmodules: -%s（声明里关掉了）\n", m.dir)
				continue
			}
			kept = append(kept, m)
		}
		modules = kept
	}
	ext, err := scanExt(root)
	if err != nil {
		fail(err)
	}
	all := append(modules, ext...)
	// 生成物必须**自己**就是 gofmt 干净的：`make check-fmt` 在 CI 里独立跑，
	// 一个没对齐的 import 块会让它红，而报的是「格式不对」——离真因（别名长度
	// 变了）隔着好几步。所以这里不靠手写缩进碰运气，直接过一遍 go/format。
	// 2026-09-20 实测：ext 的别名（ext_hello / ext_simple_cli）比 mod_* 长，
	// 手写输出的 import 块当场没对齐。
	content, err := format.Source(render(all))
	if err != nil {
		fail(fmt.Errorf("生成的源码不合法（这是生成器的 bug）: %w", err))
	}
	target := filepath.Join(root, "app", genFileName)

	if *check {
		current, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(current, content) {
			fail(fmt.Errorf("%s 已过期：运行 `make generate`（或 go generate ./app）后重新提交", target))
		}
		return
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("genmodules: %d 个组件（自带 %d + 外部 %d）→ %s\n",
		len(all), len(modules), len(ext), target)
}

// moduleRoot 从当前工作目录向上找到含 go.mod 的目录。
// `go generate` 在包目录（app/）里跑，`go run ./tools/genmodules` 在 module
// 根跑，两种调用点都要能工作，所以靠 go.mod 而不是相对路径定位。
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

// scan 列出 componentsDir 下所有「是组件」的目录名，按字母序返回。
// 字母序而非目录遍历序：生成结果必须与文件系统返回顺序无关，
// 否则不同机器上 diff 会飘。
func scan(componentsDir string) ([]module, error) {
	names, err := modscan.Dirs(componentsDir)
	if err != nil {
		return nil, err
	}
	out := make([]module, 0, len(names))
	for _, name := range names {
		// 约定：目录名里的下划线在 import 别名里保留（Go 标识符允许下划线）
		rel := path.Join(modulesRelDir, name)
		out = append(out, module{dir: name, path: rel, alias: aliasFor("mod_", name)})
	}
	return out, nil
}

// scanExt 落实外部发行版声明：把 checkout 准备好，返回声明点名的那几个模块。
//
// 没有声明 = 这个发行版不带外部模块，是合法状态（返回 nil，不报错）。
// 有声明就把 Prepare 的结论原样带出去——它要么把代码拉好、要么说出为什么不行
// （拉不动 / 本机那份 checkout 与声明不是一对），**不降级成警告**：一个「声明说
// 要装、实际没装」的构建，症状是运行期少了功能，那要等到用户发现某个命令不存在
// 才暴露。
func scanExt(root string) ([]module, error) {
	repoRoot := filepath.Dir(root)
	m, ok, err := extmanifest.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	dir := extmanifest.Dir(repoRoot)
	mods := m.Modules()
	out := make([]module, 0, len(mods))
	for _, mod := range mods {
		rel := extmanifest.ModuleRel(mod)
		// rel 是**相对 go/** 的路径（它同时是 import 后缀），磁盘上要拼的是
		// checkout 根 + 模块子目录 —— 用 rel 直接拼会多出一层 modules-ext。
		if !modscan.IsComponent(filepath.Join(dir, extmanifest.ModulesSubdir, mod, "module.go")) {
			return nil, fmt.Errorf("%s 不是一个组件（缺 module.go 或没导出 New()）", rel)
		}
		out = append(out, module{dir: rel, path: rel, alias: aliasFor("ext_", mod)})
	}
	fmt.Printf("genmodules: +%d 个外部模块（发行版 %s @ %s）\n",
		len(out), m.Distribution(), shortRev(m.Resolved))
	return out, nil
}

// aliasFor 由目录名生成 import 别名。
//
// 目录名**不必是** Go 标识符：内核自己的 modules/ 用下划线（claudecode_deepseek），
// 而发行版仓库是另一拨人的目录，`simple-cli` 这种连字符名字完全正常。别名是生成
// 物，就该由生成器把它拧成合法标识符，而不是反过来要求所有人的目录改名——
// 2026-09-20 实测：不拧的话生成的是 `ext_simple-cli`，一个语法错误，
// 而它在 `make generate` 之后的第一次编译才暴露。
func aliasFor(prefix, name string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// shortRev 把提交号截短到 7 位（日志里够用，且与 git 自己的习惯一致）。
func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// render 生成组件清单源码。生成物本身也要 gofmt 干净，见下面手写的缩进。
func render(modules []module) []byte {
	const importPath = "github.com/rzbdz/newgate/go"

	var b bytes.Buffer
	b.WriteString("// Code generated by tools/genmodules. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// 这里的内容由 `make generate`（或 go generate ./app）扫描 modules/ 得到。\n")
	b.WriteString("// 想装一个模块就把它的目录复制进 modules/、重新编译；不要手工编辑本文件。\n")
	b.WriteString("// 外部发行版的模块（modules-ext/）也在同一份清单里，装哪些由仓库根的\n")
	b.WriteString("// modules-ext.json 点名，见 tools/extmanifest。\n")
	b.WriteString("//\n")
	b.WriteString("// ⚠ 清单里若有 modules-ext/ 下的包，编译需要那份 checkout；缺了先跑 make generate\n")
	b.WriteString("// （它会按 modules-ext.json 把代码拉下来）。\n")
	b.WriteString("\npackage app\n\n")
	b.WriteString("import (\n")
	b.WriteString("\tmodules \"" + importPath + "/component\"\n")
	for _, m := range modules {
		fmt.Fprintf(&b, "\t%s \"%s/%s\"\n", m.alias, importPath, m.path)
	}
	b.WriteString(")\n\n")
	b.WriteString("// generatedComponents 是扫描 modules/ 得到的装配清单。\n")
	b.WriteString("// 顺序不是依赖声明——真实启动顺序由 capability 依赖图在构图期计算。\n")
	b.WriteString("func generatedComponents() []modules.Component {\n")
	b.WriteString("\treturn []modules.Component{\n")
	for _, m := range modules {
		fmt.Fprintf(&b, "\t\t%s.New(),\n", m.alias)
	}
	b.WriteString("\t}\n}\n")
	return b.Bytes()
}

// fail 是生成器的出口：一句话说清哪里错，退出码 1。
func fail(err error) {
	fmt.Fprintln(os.Stderr, "genmodules:", err)
	os.Exit(1)
}
