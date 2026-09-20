package locale

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// command 把语言接到界面上：`newgate lang` 看，`newgate lang <tag>` 改。
//
// 它同时是 Status / Diagnostics / Glossary 的提供者（一个对象注册四处）——
// 「谁的知识谁自己报」，与 plugin-manager 的命令同一形状。
type command struct{ svc *service }

func (c *command) Names() []string { return []string{"lang", "language"} }

func (c *command) Run(host cliapi.Host, args []string) int {
	want := cliapi.Positional(args, 0)
	if want == "" {
		c.show()
		return 0
	}
	return c.set(host, want)
}

// show 打印当前语言、它是从哪来的、以及可选语言各翻了多少。
func (c *command) show() {
	fmt.Println(style.Field(i18n.T("Language", nil), c.svc.lang+"  "+style.Dim("("+c.sourceWord()+")")))
	avail := i18n.Available()
	fmt.Println(style.Section(i18n.T("Available", nil)))
	for _, info := range avail {
		mark, line := coverageLine(info, c.svc.lang)
		fmt.Println(style.Item(mark, line))
	}
	if n := i18n.Overlaid(); n > 0 {
		fmt.Println(style.Hint(i18n.N("Overridden by {n} file in the config dir", "Overridden by {n} files in the config dir", n, i18n.A{"n": n})))
	}
}

// set 改语言：写进 state.json，并把「谁在压着它」说清楚。
func (c *command) set(host cliapi.Host, want string) int {
	tag := i18n.Normalize(want)
	if tag == "" {
		return host.Die(64, i18n.T("Usage: newgate lang <tag>  (see `newgate lang` for the list)", nil))
	}
	avail := make([]string, 0, 4)
	for _, info := range i18n.Available() {
		avail = append(avail, info.Language)
	}
	match := i18n.Match(tag, avail)
	if match == "" {
		return host.Die(65, i18n.T("No such language: {tag}. Available: {list}",
			i18n.A{"tag": want, "list": strings.Join(avail, " ")}))
	}
	if err := saveConfigured(match); err != nil {
		return host.Die(70, i18n.T("Cannot write the language to state.json: {err}", i18n.A{"err": err.Error()}))
	}
	fmt.Println(style.Item(style.OK, i18n.T("Saved to state.json: {tag}", i18n.A{"tag": match})))
	// 保存成功不等于立刻生效：环境变量与 LANG 都可能压着它（见 resolve.go 的次序）。
	// 这句话必须说——否则用户会以为没保存上，而那是最难查的一类「我明明设了」。
	if c.svc.source != SourceConfig && c.svc.lang != match {
		fmt.Println(style.Item(style.Warn, i18n.T(
			"Not in effect now: {source} overrides it in this shell (use NEWGATE_LANG={tag} for one command)",
			i18n.A{"source": string(c.svc.source), "tag": match})))
	} else if c.svc.source == SourceSystem {
		fmt.Println(style.Item(style.Warn, i18n.T(
			"Currently following the system locale; this setting will take effect once LANG/LC_ALL stop overriding it",
			nil)))
	}
	return 0
}

// sourceWord 把来源讲成人话。
func (c *command) sourceWord() string {
	switch c.svc.source {
	case SourceEnvOverride:
		return "NEWGATE_LANG"
	case SourceConfig:
		return i18n.T("from config", nil)
	case SourceSystem:
		return i18n.T("from the system locale", nil)
	}
	return i18n.T("default", nil)
}

// coverageLine 把一份体检结果排成一行：语言、覆盖率、以及该说的坏消息。
func coverageLine(info i18n.Info, current string) (string, string) {
	name := info.Language
	if info.Language == current {
		name = style.Bold(name)
	}
	pct := 100
	if info.Total > 0 {
		pct = info.Translated * 100 / info.Total
	}
	// 补齐按**显示宽度**算（style.Pad）：语言名里有中文时 %-14s 会歪。
	parts := []string{fmt.Sprintf("%s %3d%%  %d/%d", style.Pad(name, 14), pct, info.Translated, info.Total)}
	var notes []string
	if info.Language == i18n.SourceLang {
		notes = append(notes, i18n.T("source", nil))
	}
	if info.Machine > 0 {
		notes = append(notes, i18n.N("machine-translated, unreviewed: {n}",
			"machine-translated, unreviewed: {n}", info.Machine, i18n.A{"n": info.Machine}))
	}
	if n := info.Total - info.Translated; n > 0 {
		notes = append(notes, i18n.N("missing: {n}", "missing: {n}", n, i18n.A{"n": n}))
	}
	mark := style.OK
	if len(notes) > 0 {
		mark = style.Warn
	}
	return mark, strings.Join(append(parts, notes...), "  ")
}
