// Package locale 是**消息目录**：用户可见文本按语言查表。
//
// # 为什么是「共享叶子」而不是一个 capability
//
// 查表无状态、无生命周期、没有所有者——装几份都一样，谁都能引（判据见
// docs/03-architecture.md §2 的注，同 config/paths、lib/style）。capability 那条
// 禁令说的是「有所有者、要排启动顺序、要在 Stop 里撤销」的服务；一张消息表不是
// 那种东西。所以这里的包级函数是被允许的，而 modules/locale 那个组件只负责
// **决定装哪一门语言**（读环境变量与配置）。
//
// # 键 = 源语言原文（msgid），不是符号
//
// 键就是代码里写的那句英文：`locale.T("Proxy", nil)`。这是 gettext 的老规矩，
// 在这里它买到的是**一整类故障的消失**——没有键就没有「键写错了」，
// 也就不需要「常量 ↔ 目录 ↔ 调用点」三方对账那套容易腐坏的机制。
//
// 代价说清楚：改动英文措辞 = 旧译文变孤儿（`tools/i18n check` 会报出来，不静默）。
// 需要「同一句话两种译文」时，做法是**把英文写得更具体**（gettext 的老办法），
// 而不是引入键。
//
// 源语言（en）在运行时**不查表**：代码里的那句英文就是最终文本，`T` 只做占位符
// 替换。所以「默认语言 = 源语言」这条路径是零成本的，输出与加 i18n 之前逐字节相同。
//
// # 为什么自研而不是引 golang.org/x/text
//
// 内核的构建与测试必须完全离线（CI 跑 `GOPROXY=off go test ./...`，那条约束被
// docs + CI + 测试共同钉住）。同一把尺子下的先例：lib/style 的 runeWidth 是自研的
// East Asian Width 子集。这里只做四件事：查表、具名占位符替换、单/复数二选一、
// 语言标签匹配——够用就够。
package i18n

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// SourceLang 是**代码里写的那一门语言**：源语言。它没有目录文件——
// 它的「表」就是源码本身（见 tools/i18n 生成的账本 catalogs/en.json，那份只用于
// 统计与给译者看，运行时用不上）。
const SourceLang = "en"

// A 是具名占位符的实参。用具名（`{dir}`）而不是位置（`%s`）是为了**翻译**：
// 英文和中文的语序不同，具名占位符让译者自由重排整句（`%[1]s` 那种写法能重排，
// 但没人愿意在译文里数数字）。
type A map[string]any

// Entry 是一条**译文**。
//
// 只做 one/other 两级，不做完整 CLDR：这个 CLI 里带数量的句子，英文只有
// 「1 个」与「不止 1 个」两种形态，中文压根不分。真需要第三级的语言再谈——
// 那时该重新想的是要不要引 CLDR，而不是在这里堆特例。
type Entry struct {
	Text  string // 无复数变化的译文
	One   string // 复数：1
	Other string // 复数：其余

	Note     string // 给译者看的上下文（不进二进制输出）
	Machine  bool   // 机翻产物，还没人工复核
	Reviewed bool   // 人工复核过
}

// plural 这条译文是不是按数量变形。
func (e Entry) plural() bool { return e.One != "" || e.Other != "" }

// Empty 没翻（或翻成空）的条目等同于没有——工具与覆盖率统计都拿它当判据。
func (e Entry) Empty() bool { return e.Text == "" && !e.plural() }

// Catalog 是一门语言的翻译表。Messages 的键是**源语言原文**。
type Catalog struct {
	Language string
	Source   string         // 翻译自哪一门（源语言那份留空）
	Widths   map[string]int // 版式尺寸（见 Width）：label / term 之类
	Messages map[string]Entry
	Builtin  bool // 编译进二进制的，还是磁盘上捡来的
}

// Ledger 是**账本**：源语言里有哪些消息、各带什么实参、写在哪个文件的哪一行。
//
// 它是生成物（`tools/i18n extract` 写、`-check` 校验），用途是**统计与校对**：
// 覆盖率的分母、占位符一致性、孤儿键检测、给翻译工具的输入。运行时也嵌一份
// （小），这样 doctor 能直接报「翻了多少、还缺多少」，而不必去读仓库。
type Ledger struct {
	Messages map[string]LedgerEntry
}

// LedgerEntry 是账本里的一条。
type LedgerEntry struct {
	Where string   // 源码位置（生成物）
	Args  []string // 调用点给的实参名（生成物）
	One   string   // 复数消息的单数形式（源码里的那一份）
	Other string   // 复数消息的复数形式
	Note  string   // 人写的上下文（extract 绝不覆盖它）
}

// Plural 这条消息是否按数量变形。
func (e LedgerEntry) Plural() bool { return e.One != "" || e.Other != "" }

// Info 是一门语言的**体检结果**，给 `newgate lang` 与 doctor 看。
//
// 为什么要有它：加了多语言之后「这门语言到底翻全了没有」必须能一句话答上来。
// 缺翻译本身就是一个值得说出来的事实——回退到英文是 fail-open，不是静默。
type Info struct {
	Language   string
	Builtin    bool
	Total      int // 账本里有多少条
	Translated int // 这门语言翻了多少条
	Machine    int // 其中机翻未复核
	Reviewed   int // 其中人工复核过
}

// ---------- 已装好的状态 ----------
//
// 一次进程只装一门语言，装完不再变（Install 由 modules/locale 在 Start 里调一次）。
// 读路径因此只加一把读锁：`newgate status` 一次要查几十条，转发热路径上的日志也
// 要查——那里每多一次分配都值钱。

var (
	mu       sync.RWMutex
	current  = SourceLang
	table    = map[string]Entry{} // 当前语言的译文（源语言时为空，走恒等路径）
	ledger   = map[string]LedgerEntry{}
	widths   = map[string]int{}
	builtins = map[string]Catalog{}
	overlaid int // 有几个键来自**磁盘覆盖**
)

// Install 装配这次进程要用的语言，返回实际生效的 tag。
//
// led 是账本（源语言有哪些消息）；catalogs 是各语言的译本（含源语言自己那份空的
// 也行）；diskDir 非空时从那里读 `<tag>.json` 覆盖内置——「用户丢个文件就能改一句
// 翻译、不必重编译」，同 `~/.config/newgate/mappings/` 的文化。
//
// 请求的语言不存在时**回退到源语言**而不是报错：一个 LANG=fr_FR 的用户不该因为
// 我们没翻法语就用不了 newgate。回退在 Info 与 Missing 里看得见。
func Install(tag string, led Ledger, catalogs []Catalog, diskDir string) (string, error) {
	builtins = map[string]Catalog{}
	for _, c := range catalogs {
		c.Builtin = true
		if c.Messages == nil {
			c.Messages = map[string]Entry{}
		}
		builtins[c.Language] = c
	}

	avail := make([]string, 0, len(builtins)+1)
	for lang := range builtins {
		avail = append(avail, lang)
	}
	if _, ok := builtins[SourceLang]; !ok {
		avail = append(avail, SourceLang) // 源语言永远可选（它不需要目录文件）
	}
	sort.Strings(avail)

	eff := Match(tag, avail)
	if eff == "" {
		eff = SourceLang
	}

	next := map[string]Entry{}
	overlaid = 0
	if eff != SourceLang {
		for id, e := range builtins[eff].Messages {
			if !e.Empty() {
				next[id] = e
			}
		}
		if raw, ok := readOverlay(diskDir, eff); ok {
			for id, e := range raw.Messages {
				if _, known := led.Messages[id]; !known {
					continue // 覆盖只能改已有的消息，凭空多出来的（拼错了）不生效
				}
				if e.Empty() {
					continue
				}
				next[id] = e
				overlaid++
			}
		}
	}

	mu.Lock()
	current, table, ledger = eff, next, led.Messages
	widths = builtins[eff].Widths
	if widths == nil {
		widths = builtins[SourceLang].Widths
	}
	mu.Unlock()
	return eff, nil
}

// Extend 把另一份账本与目录**并进**这次已经装好的装配。
//
// 为什么需要它：Install 干的是「解析语言 + 重建整张表」，一次装配只该发生一次
// （那是 modules/locale 的活）。而发行版是**另一个 Go module**，它自己的模块有
// 自己的界面文案——那些 id 既不在内核的账本里，也不在内核的目录里。让发行版再
// 调一次 Install 会把内核那份整份冲掉；所以：安装归安装，追加归追加。
//
// 调用时机：modules/locale 的 Start 之后。发行版模块写 `Need(localeapi.Capability)`
// 就拿到了这条顺序边。语言此刻已经定下来，这里只补消息，**不再解析一遍**——不然
// 两个地方各有一套「谁压过谁」的优先级，迟早对不上。
//
// **同 id 不覆盖**：内核已经说过的消息，追加改不动它。要让发行版改内核的措辞，
// 正路是改内核，或者走磁盘覆盖（`~/.config/newgate/locale/<tag>.json` 的语义本来
// 就是「盖掉同名键」）。这条保住了「谁拥有这条消息，谁定它的译文」。
//
// 账本一起并：`newgate lang` 的覆盖率、`Missing` 的缺口都要把发行版那些消息算进去，
// 否则发行版的一半界面在仪表盘上根本不存在。
//
// `meta.widths` 不并：版式尺寸是**语言**的属性，归内核目录（同一门语言在内核与
// 发行版里该是同一套列宽）。
func Extend(led Ledger, catalogs []Catalog) error {
	mu.Lock()
	defer mu.Unlock()
	for _, c := range catalogs {
		if c.Language == "" {
			return fmt.Errorf("the appended catalog has no meta.language")
		}
		b, known := builtins[c.Language]
		if !known {
			b = Catalog{Language: c.Language, Source: SourceLang}
		}
		if b.Messages == nil {
			b.Messages = map[string]Entry{}
		}
		for id, e := range c.Messages {
			if e.Empty() {
				continue
			}
			if _, exists := b.Messages[id]; exists {
				continue // 内核已经说过的话，追加不改
			}
			b.Messages[id] = e
			// 源语言那条路径是**恒等**的（不查表，代码里写的就是最终文本）。
			// 往表里写一条等于把它变成「查表的结果」——那性质就没了。
			if c.Language == current && current != SourceLang {
				table[id] = e
			}
		}
		builtins[c.Language] = b
	}
	for id, e := range led.Messages {
		if _, exists := ledger[id]; !exists {
			ledger[id] = e
		}
	}
	return nil
}

// T 查一条消息并填上具名占位符。
//
// 源语言（或没翻到）时**直接用传进来的那句话**——这不是「回退」，是恒等：
// 代码里写的就是最终文本，一次 map 查表都没发生（未命中即恒等）。
func T(msg string, args A) string { return Tn(msg, "", 0, args) }

// N 是带数量的消息：msg 是**单数**形式（账本与译文都以它为键），plural 是复数形式。
//
// 为什么两个形式都要给：源语言在运行时是恒等路径，程序得知道英文的复数怎么写；
// 译文那边则以单数形式为键查表（gettext 的老规矩）。中文的译文只写 other 即可。
func N(msg, plural string, n int, args A) string { return Tn(msg, plural, n, args) }

func Tn(msg, plural string, n int, args A) string {
	mu.RLock()
	e, ok := table[msg]
	lang := current
	mu.RUnlock()

	if !ok || e.Empty() {
		// 没翻（或就是源语言）：用**调用点写的那句**，只做占位符替换。
		// 源语言的单复数形式就在调用点上（`N(msg, plural, n, …)` 两个都给了），
		// 所以这条路径不需要任何目录数据——这正是「默认语言 = 源语言」零成本的原因。
		if plural != "" {
			return format(pickSource(msg, plural, n), withN(args, n))
		}
		return format(msg, args)
	}
	if !e.plural() {
		return format(e.Text, args)
	}
	if e.One == "" || isChinese(lang) {
		// 中文没有单复数变化：只写 other 就是全部。
		if e.Other != "" {
			return format(e.Other, withN(args, n))
		}
		return format(e.One, withN(args, n))
	}
	if n == 1 {
		return format(e.One, withN(args, n))
	}
	return format(e.Other, withN(args, n))
}

// pickSource 源语言的单复数选择：1 用单数，其余用复数（英文的规则）。
func pickSource(one, other string, n int) string {
	if n == 1 {
		return one
	}
	if other != "" {
		return other
	}
	return one
}

// withN 把数量塞进实参：译文里的 `{n}` 与调用点自己给的 `n` 都认。
func withN(args A, n int) A {
	if _, ok := args["n"]; ok {
		return args
	}
	out := make(A, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out["n"] = n
	return out
}

// Error 是带**消息身份**的错误：文案在 Error() 里现渲染。
//
// 为什么不让调用点写 `errors.New(locale.T(...))`：那样测试只能断言渲染出来的
// 那句话（中文散文），一换语言或改措辞就红。这里 ID 是语言无关的，
// 测试写 `if locale.ID(err) == "cannot read mappings: {err}"`——断言的是身份，
// 不是译文。
type Error struct {
	Msg  string // 源语言原文（= 消息身份）
	Args A
	Err  error // 可选的底层错误（%w 语义）
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		args := A{}
		for k, v := range e.Args {
			args[k] = v
		}
		args["err"] = e.Err.Error()
		return T(e.Msg, args)
	}
	return T(e.Msg, e.Args)
}

func (e *Error) Unwrap() error { return e.Err }

// E 造一条可翻译的错误。args 里可以不放 "err"，也可以放（那就不必再用 %w）。
func E(msg string, args A) error { return &Error{Msg: msg, Args: args} }

// Ef 是「包住一个底层错误」的写法：`locale.Ef(err, "cannot read {path}", …)`。
func Ef(err error, msg string, args A) error {
	if err == nil {
		return nil
	}
	return &Error{Msg: msg, Args: args, Err: err}
}

// ID 取一条错误的**消息身份**（源语言原文），测试与日志用它做语言无关的断言。
// 不是 *Error 的错误返回它自己的 Error() 文本（那是别人家的错，原样透传）。
func ID(err error) string {
	var le *Error
	if errors.As(err, &le) {
		return le.Msg
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------- 给上层看的查询接口 ----------

// Current 当前生效的语言 tag。
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Width 这门语言下标定的版式尺寸（列宽），没标定就用调用点给的缺省值。
//
// 为什么宽度是**语言数据**：`Field("代理", …)` 的标签列在中文下 8 列够，换成
// 英文（"Requests"/"Takeover"）就不够了——不是代码写错，是语言变了。把它放进
// 目录的 meta 里，加一门语言就连它的版式一起标定。
func Width(kind string, fallback int) int {
	mu.RLock()
	defer mu.RUnlock()
	if w, ok := widths[kind]; ok && w > 0 {
		return w
	}
	return fallback
}

// Available 列出可选语言各自的体检结果，按 tag 排序。源语言排在最前。
func Available() []Info {
	mu.RLock()
	total := len(ledger)
	langs := make([]string, 0, len(builtins)+1)
	for lang := range builtins {
		langs = append(langs, lang)
	}
	if _, ok := builtins[SourceLang]; !ok {
		langs = append(langs, SourceLang)
	}
	sort.Strings(langs)
	builtinsCopy := map[string]Catalog{}
	for k, v := range builtins {
		builtinsCopy[k] = v
	}
	mu.RUnlock()

	out := make([]Info, 0, len(langs))
	for _, lang := range langs {
		info := Info{Language: lang, Builtin: true, Total: total}
		if lang == SourceLang {
			info.Translated = total
			out = append(out, info)
			continue
		}
		for id, e := range builtinsCopy[lang].Messages {
			le, known := ledgerOf(id)
			if !known || e.Empty() {
				continue
			}
			_ = le
			info.Translated++
			switch {
			case e.Reviewed:
				info.Reviewed++
			case e.Machine:
				info.Machine++
			}
		}
		out = append(out, info)
	}
	return out
}

func ledgerOf(id string) (LedgerEntry, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := ledger[id]
	return e, ok
}

// Missing 这门语言还缺哪些消息（相对账本）。给 doctor 与 `newgate lang` 用。
func Missing(lang string) []string {
	mu.RLock()
	defer mu.RUnlock()
	if lang == SourceLang {
		return nil
	}
	c, ok := builtins[lang]
	out := make([]string, 0, len(ledger))
	for id := range ledger {
		e, ok2 := c.Messages[id]
		if !ok || !ok2 || e.Empty() {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Overlaid 这次装配里有几个键来自磁盘覆盖（0 表示没人在改翻译）。
func Overlaid() int {
	mu.RLock()
	defer mu.RUnlock()
	return overlaid
}

// ---------- 小工具 ----------

func isChinese(lang string) bool { return primary(Normalize(lang)) == "zh" }

// format 替换 `{name}`。认不出实参的占位符**原样留着**：界面上出现 `{dir}`
// 是难看的，但它精确指出「这里少给了一个参数」，比悄悄变成空串强。
func format(text string, args A) string {
	if len(args) == 0 || !strings.ContainsRune(text, '{') {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		c := text[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(text[i:], '}')
		if end < 0 {
			b.WriteString(text[i:])
			break
		}
		name := text[i+1 : i+end]
		v, ok := args[name]
		if !ok || !placeholderName(name) {
			b.WriteString(text[i : i+end+1])
			i += end + 1
			continue
		}
		b.WriteString(render(v))
		i += end + 1
	}
	return b.String()
}

func render(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
