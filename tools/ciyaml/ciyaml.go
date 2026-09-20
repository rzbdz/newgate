// Package ciyaml 检查 .github/workflows/*.yml 的**结构**——不做完整 YAML 解析，
// 只认「按缩进一眼能看出来的硬伤」。
//
// 为什么值得单独一层（2026-09-20 的教训）：摊平 go/ 那次改 workflow，把一个步骤
// 的 `run:` 行连同 `working-directory` 一起删掉了，留下这么一条：
//
//   - name: opencode 侧端到端
//
// 既没有 `run` 也没有 `uses`。这份 workflow 从此**非法**，GitHub 在 0 秒拒掉
// 整个 run——一个 job 都没起。于是接下来六个提交的 CI 全是红的，看着像
// 「测试挂了」，实际是**测试根本没跑**。这种失败必须有人喊，而喊的人不能是
// GitHub：等它开口，东西已经推上去了。
//
// 所以把同一件事挪到**本地**——内核的 `go test ./...`（app/ci_test.go）与发行版的
// dist-test（testing/ci_test.go）都会跑它。判据刻意保守：宁可漏报也不误报，
// 因为一条会误报的棘轮最后一定被人绕过去。
//
// 完整解析要引 YAML 库，而这个仓库**不许有第三方依赖**——同一个取舍下已经有
// lib/style 手写的 East Asian Width 与 tools/i18n 手写的扫描器。
package ciyaml

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding 一条结构问题。没有 Level：这里报的全是 GitHub 会拒掉整个 workflow 的
// 硬伤——要么全红，要么全不红，分成两级只会让人以为「warn 可以先不管」。
type Finding struct {
	File string // 相对 root 的路径
	Line int    // 1 起
	Msg  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Msg)
}

// Check 扫描 root/.github/workflows 下的全部 *.yml/*.yaml。
//
// 目录不存在、或者一个 workflow 都没有时返回错误而不是空结果：那是**判据自己
// 退化了**，不是「仓库很干净」。空结果必须只意味着「查过了，没问题」。
func Check(root string) ([]Finding, error) {
	dir := filepath.Join(root, ".github", "workflows")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读不了 %s: %w", dir, err)
	}
	var files []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") {
			files = append(files, filepath.Join(dir, n))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s 里一个 workflow 都没有——判据退化了", dir)
	}
	sort.Strings(files)

	var out []Finding
	for _, f := range files {
		fs, err := checkFile(root, f)
		if err != nil {
			return nil, err
		}
		out = append(out, fs...)
	}
	return out, nil
}

// yline 一行有效内容（空行与整行注释已经滤掉），带原始缩进列数。
type yline struct {
	no     int
	indent int
	text   string // 已 TrimSpace
}

func (l yline) isItem() bool { return l.text == "-" || strings.HasPrefix(l.text, "- ") }

func checkFile(root, path string) ([]Finding, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读不了 %s: %w", path, err)
	}
	rel, rerr := filepath.Rel(root, path)
	if rerr != nil {
		rel = path
	}

	var (
		out   []Finding
		lines []yline
	)
	for i, s := range strings.Split(string(raw), "\n") {
		no := i + 1
		indent := 0
		for indent < len(s) && s[indent] == ' ' {
			indent++
		}
		// YAML 的缩进不许用 tab。这行要是混进去了，报错信息会指向一个
		// 完全无关的列号，查起来很费劲——这里直接点名。
		if indent < len(s) && s[indent] == '\t' {
			out = append(out, Finding{rel, no, "缩进里出现了 tab——YAML 不认 tab 缩进"})
		}
		text := strings.TrimSpace(s)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		lines = append(lines, yline{no: no, indent: indent, text: text})
	}

	// 空值：`run:` 后面什么都没有（下一行没有缩进更深的子键）。这就是当初那个
	// bug 的另一半——三个 job 的 `defaults: run:` 被删得只剩壳。
	for i, l := range lines {
		key, ok := emptyableKey(l.text)
		if !ok {
			continue
		}
		if i+1 < len(lines) && lines[i+1].indent > l.indent {
			continue // 有子键，不是空值
		}
		out = append(out, Finding{rel, l.no,
			fmt.Sprintf("「%s:」是空值——要么补上值，要么整段删掉", key)})
	}

	// jobs: 之下逐个 job 看。找不到 jobs: 就是整个文件白写了。
	jobsAt := -1
	for i, l := range lines {
		if l.indent == 0 && l.text == "jobs:" {
			jobsAt = i
			break
		}
	}
	if jobsAt < 0 {
		return append(out, Finding{rel, 1, "顶层没有 jobs:——这个 workflow 不会做任何事"}), nil
	}
	rest := lines[jobsAt+1:]
	if len(rest) == 0 {
		return append(out, Finding{rel, lines[jobsAt].no, "jobs: 下面是空的"}), nil
	}

	// job 名 = 紧跟 jobs: 之后、与第一个键同缩进的那些 `名字:` 行。
	jobIndent := rest[0].indent
	for i := 0; i < len(rest); {
		if rest[i].indent != jobIndent || !strings.HasSuffix(rest[i].text, ":") {
			i++
			continue
		}
		name := strings.TrimSuffix(rest[i].text, ":")
		end := i + 1
		for end < len(rest) && rest[end].indent > jobIndent {
			end++
		}
		out = append(out, checkJob(rel, rest[i:end], name)...)
		i = end
	}
	return out, nil
}

// checkJob 看一个 job：必须有 runs-on（否则 GitHub 拒），steps 里每一步必须有
// run 或 uses。
func checkJob(rel string, body []yline, name string) []Finding {
	var out []Finding
	hasRunsOn := false
	stepsAt := -1
	for i, l := range body {
		if strings.HasPrefix(l.text, "runs-on:") && l.indent > body[0].indent {
			hasRunsOn = true
		}
		if l.text == "steps:" {
			stepsAt = i
		}
	}
	if !hasRunsOn {
		out = append(out, Finding{rel, body[0].no,
			fmt.Sprintf("job「%s」没有 runs-on——GitHub 会拒掉整个 workflow", name)})
	}
	if stepsAt < 0 {
		return append(out, Finding{rel, body[0].no,
			fmt.Sprintf("job「%s」没有 steps", name)})
	}

	items := body[stepsAt+1:]
	if len(items) == 0 {
		return append(out, Finding{rel, body[stepsAt].no,
			fmt.Sprintf("job「%s」的 steps: 下面是空的", name)})
	}
	itemIndent := items[0].indent
	for i := 0; i < len(items); {
		if items[i].indent != itemIndent || !items[i].isItem() {
			i++
			continue
		}
		end := i + 1
		for end < len(items) && items[end].indent > itemIndent {
			end++
		}
		if f, bad := checkStep(rel, items[i:end], itemIndent); bad {
			out = append(out, f)
		}
		i = end
	}
	return out
}

// checkStep 一步必须有 run 或 uses。键两个地方都算：`- uses: x` 写在破折号那一行，
// 或者写在下面同级的 `run:` / `uses:`。更深的键（`with:` 的子键）不算——那层是
// 参数，不是这一步的类型。
func checkStep(rel string, item []yline, itemIndent int) (Finding, bool) {
	hasBody, stepName := false, ""
	dash := strings.TrimSpace(strings.TrimPrefix(item[0].text, "-"))
	if k, v, ok := splitKey(dash); ok {
		switch k {
		case "run", "uses":
			hasBody = true
		case "name":
			stepName = v
		}
	}
	for _, l := range item[1:] {
		if l.indent != itemIndent+2 {
			continue
		}
		k, v, ok := splitKey(l.text)
		if !ok {
			continue
		}
		switch k {
		case "run", "uses":
			hasBody = true
		case "name":
			stepName = v
		}
	}
	if hasBody {
		return Finding{}, false
	}
	what := "这一步"
	if stepName != "" {
		what = fmt.Sprintf("步骤「%s」", stepName)
	}
	return Finding{rel, item[0].no,
		what + "既没有 run 也没有 uses——GitHub 会拒掉整个 workflow，一个 job 都不会起"}, true
}

// emptyableKey 返回「这个键必须带值」的键名。值是空的（下一行没有更深的子键）
// 就是硬伤：`run:` 空着等于这一步什么都不做。
func emptyableKey(text string) (string, bool) {
	for _, k := range []string{"run", "shell", "uses", "runs-on", "steps"} {
		if text == k+":" {
			return k, true
		}
	}
	return "", false
}

// splitKey 把 `name: 值` 拆成键与值；不是「键: 值」形态就返回 ok=false。
// 值里可能带冒号（`run: echo a:b`），所以只切第一个。
func splitKey(s string) (key, val string, ok bool) {
	i := strings.Index(s, ":")
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:i])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.TrimSpace(s[i+1:]), true
}
