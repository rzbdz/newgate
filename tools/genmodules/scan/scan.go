// Package scan 是「哪些目录算组件」这条判据的**唯一实现**。
//
// 为什么单独成包（2026-09-18）：判据原来有两份，而且口径不同——生成器
// （tools/genmodules）用 AST 看返回值是不是 `X.Component`，而 app 的装配测试
// （app/modules_gen_test.go 的 moduleDirs）用 `bytes.Contains` 找字面量
// `func New() modules.Component`。后果是：照文档写 `func New() component.Component`
// 的模块会被生成器装进清单（AST 认），却数不进测试的目录集合，于是测试报
// 「装配了 N 个组件，modules/ 下有 M 个目录」——一个完全指不到真因的失败。
//
// 现在两份都调这里。生成器是**构建期**工具（只解析不编译），测试是装配期断言，
// 两者必须用同一把尺子，否则它们的分歧会伪装成别的问题冒出来。
package scan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dirs 列出 componentsDir 下所有「是组件」的目录名，按字母序返回。
//
// 字母序而非目录遍历序：生成结果必须与文件系统返回顺序无关，否则不同机器上
// diff 会飘。
func Dirs(componentsDir string) ([]string, error) {
	entries, err := os.ReadDir(componentsDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !IsComponent(filepath.Join(componentsDir, entry.Name(), "module.go")) {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out, nil
}

// Ident 把目录名拧成一个合法的 Go 标识符（前缀保证首字符合法），
// 例如 `simple-cli` → `mod_simple_cli`。
//
// 为什么由生成器拧、而不是要求目录改名：目录名**不必是** Go 标识符。内核自己的
// modules/ 用下划线（claudecode_deepseek），而发行版仓库是另一拨人的目录，
// `simple-cli` 这种连字符名字完全正常。别名是生成物，就该由生成器拧好。
// 2026-09-20 实测：不拧的话生成的是 `ext_simple-cli`——一个语法错误，而它要到
// `make generate` 之后的第一次编译才暴露。
//
// 与「哪些目录算组件」同址（判据只有一份）：内核与发行版各有一个生成器，
// 两边必须用同一把尺子，否则同一个目录名会在两个仓库里拧出两个不同的标识符。
func Ident(prefix, dir string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// isComponent 判断一个 module.go 是否导出 `func New() modules.Component`。
// 只解析不编译：这一步在构建之前跑，不能依赖源码当时能通过类型检查。
func IsComponent(path string) bool {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "New" || fn.Recv != nil {
			continue
		}
		results := fn.Type.Results
		if results == nil || len(results.List) != 1 {
			continue
		}
		sel, ok := results.List[0].Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Component" {
			continue
		}
		return true
	}
	return false
}
