// Package scan 是「源码里有哪些可翻译消息」这条判据的**唯一实现**。
//
// 为什么要单独成包（同 tools/genmodules/scan 那条理由）：内核与发行版各有一个
// `tools/i18n`（发行版通过 replace 拿到这个包），两边必须用同一把尺子——否则
// 同一个调用点会在两个仓库里被数成两条消息、或者一边数得到一边数不到。
//
// 判据是**键 = 源语言原文**（见 lib/i18n 的包注释）：扫的就是 `i18n.T("...")`
// 这类调用里的字符串字面量。所以这里没有「消息 ID 表」，也不可能有——没有键，
// 就没有键写错这回事。
package scan

import (
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
)

// Call 是一次可翻译调用。
type Call struct {
	Msg    string   // 消息原文（= 键）
	Method string   // T / N / E / Ef —— 检查实参时要区别对待（N 注入 n、Ef 注入 err）
	Plural string   // N() 的复数形式；空 = 这条不分单复数
	Args   []string // 调用点给的实参名（`i18n.A{"dir": …}` 的键）
	Where  string   // 相对仓库根的 `文件:行`
}

// SkipDir 判一棵源码树里哪些目录**不该走进去**。
//
// 两条判据共用它：本包的 AST 扫描（哪些消息）与 check.CJKFiles 的中文清点
// （哪些文件还没迁）。两把尺子量的必须是**同一棵树**——不然会出现「账本里没有
// 这个文件、豁免清单里却有它」这种自相矛盾，而那是排查起来最费劲的一类。
//
// 两类目录被跳过：
//   - 产物与外部代码（`.git`/`bin`/`dist`/`vendor`/`node_modules`）；
//   - **嵌套的 Go module**——发行版的 `core/` 就是内核那份 submodule，它自带
//     go.mod，是另一个产品。扫进别人家去，会把内核的消息算进发行版的账本。
//     判据用 go.mod 而不是目录名：`core` 只是今天的惯例，「这里是不是另一个
//     module」才是这件事本身。
func SkipDir(root, path string) bool {
	switch filepath.Base(path) {
	case ".git", "bin", "dist", "node_modules", "vendor":
		return true
	}
	if path != root {
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			return true
		}
	}
	return false
}

// Dir 扫一棵源码树，返回全部可翻译调用（按位置排序）。
//
// 只扫**非测试**源码：测试里的字面量是断言用的夹具，不是给用户看的文案。
func Dir(root string) ([]Call, error) {
	var out []Call
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipDir(root, p) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if strings.HasSuffix(p, "_gen.go") {
			// 生成物（装配清单之类）里不会有给用户看的文案；即便有，它也该在
			// 生成器那边改。整份跳过，理由与 app/direction_test.go 跳过它一致。
			return nil
		}
		file, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		pkg := i18nAlias(file)
		if pkg == "" {
			return nil // 这个文件没引 lib/i18n
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = p
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			found, cerr := parseCall(fset, call, pkg, rel)
			if cerr != nil {
				out = append(out, Call{Msg: "", Where: cerr.Error()}) // 见下面的约定
				return true
			}
			if found != nil {
				out = append(out, *found)
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Where != out[j].Where {
			return out[i].Where < out[j].Where
		}
		return out[i].Msg < out[j].Msg
	})
	return out, nil
}

// i18nAlias 找这个文件里 lib/i18n 的本地名字（`i18n` 是惯例，但别假设）。
func i18nAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasSuffix(path, "/lib/i18n") {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "i18n"
	}
	return ""
}

// parseCall 认一条可翻译调用。返回 nil 表示这次调用与 i18n 无关。
func parseCall(fset *token.FileSet, call *ast.CallExpr, pkg, rel string) (*Call, error) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != pkg {
		return nil, nil
	}
	pos := fset.Position(call.Pos())
	where := fmt.Sprintf("%s:%d", rel, pos.Line)

	var msgIdx, pluralIdx int
	switch sel.Sel.Name {
	case "T", "E":
		msgIdx, pluralIdx = 0, -1
	case "Ef": // Ef(err, msg, args)
		msgIdx, pluralIdx = 1, -1
	case "N": // N(msg, plural, n, args)
		msgIdx, pluralIdx = 0, 1
	default:
		return nil, nil
	}
	if len(call.Args) <= msgIdx {
		return nil, fmt.Errorf("%s: %s() 没给消息", where, sel.Sel.Name)
	}
	msg, ok := literal(call.Args[msgIdx])
	if !ok {
		return nil, fmt.Errorf("%s: %s() 的第一个参数不是字符串字面量——"+
			"消息必须是字面量，拼接出来的句子没法翻译（也不该翻译：那是数据不是文案）",
			where, sel.Sel.Name)
	}
	if strings.TrimSpace(msg) == "" {
		return nil, fmt.Errorf("%s: %s() 的消息是空串", where, sel.Sel.Name)
	}
	out := &Call{Msg: msg, Method: sel.Sel.Name, Where: where}
	if pluralIdx >= 0 && len(call.Args) > pluralIdx {
		plural, ok := literal(call.Args[pluralIdx])
		if !ok {
			return nil, fmt.Errorf("%s: N() 的复数形式不是字符串字面量", where)
		}
		out.Plural = plural
	}
	// 实参：`i18n.A{"dir": …}` 的键。取**最后一个**参数里的具名键——
	// 契约就是「args 是最后一个参数」（见 lib/i18n 的 A 类型）。
	for i := len(call.Args) - 1; i >= 0; i-- {
		if keys := compositeKeys(call.Args[i]); keys != nil {
			out.Args = keys
			break
		}
	}
	return out, nil
}

// literal 取一个**编译期常量字符串**：字面量，或字面量的 `+` 拼接。
//
// 为什么要认拼接：长句子在源码里必然要折行，写成
//
//	i18n.T("the proxy is not running (newgate start) — "+
//		"run `newgate start` first")
//
// 是 Go 里唯一的折行办法。它的**值**就是一个字面量，翻译起来与写成一行没有区别——
// 而按行扫描会把这种人眼最正常不过的写法误报成「消息不是字面量」。折叠在前，判据在后。
//
// 只折叠字面量（含嵌套拼接）。引用**常量标识符**不折叠：那要跨文件求值，
// 而「消息就在调用点上、看得见」正是 msgid-as-key 的立身之本——真需要复用，
// 复用的该是译文，不是英文原文。
func literal(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		left, ok := literal(v.X)
		if !ok {
			return "", false
		}
		right, ok := literal(v.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	}
	return "", false
}

// compositeKeys 取一个复合字面量的具名键（`A{"dir": x, "n": y}` → dir, n）。
// 不是复合字面量（或用了位置写法）时返回 nil。
func compositeKeys(e ast.Expr) []string {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil // 位置写法：拿不到名字，交给上层按「没有实参」处理
		}
		key, ok := kv.Key.(*ast.BasicLit)
		if !ok || key.Kind != token.STRING {
			return nil
		}
		s, err := strconv.Unquote(key.Value)
		if err != nil {
			return nil
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Messages 把扫描结果收成「消息 → 那次调用」的表。同一条消息在多处出现是正常的
// （同一句话在几个地方用），取**第一次**出现的位置作为账本里的 where。
func Messages(calls []Call) map[string]Call {
	out := make(map[string]Call, len(calls))
	for _, c := range calls {
		if c.Msg == "" {
			continue // 扫描时记下的错误，交给调用方单独报
		}
		if _, seen := out[c.Msg]; !seen {
			out[c.Msg] = c
		}
	}
	return out
}

// Problems 取出扫描时记下的错误（Msg 为空、Where 里写着原因）。
func Problems(calls []Call) []string {
	var out []string
	for _, c := range calls {
		if c.Msg == "" {
			out = append(out, c.Where)
		}
	}
	return out
}
