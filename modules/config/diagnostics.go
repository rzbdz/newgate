package config

// 本文件把**配置自己的**三条展示面报给界面：doctor 的两项体检（文件、链路）、
// status 的「配置」一行，以及 status 里那两张表（档位绑定、fallback 链）。
//
// **为什么它们住在这里**（2026-09-18）：这几段以前写在 modules/cli/diag.go 里，
// 于是界面为了渲染它们得 import config/{paths,store,resolve,domain}——profile
// 怎么解析、链怎么建、哪些键算合法，全是配置的知识，却要界面替它算。用户的原话：
// 「cli 目录里面不能包含任何和 cli 展示无关的所有东西」。谁的状态谁自己报，界面
// 只负责循环调用与排版（见 cli/extension 的 StatusLine / StatusBlock）。
//
// 依赖方向：config → cli/extension（叶子契约），不是 → modules/cli。所以这是
// 一次普通的「owner 往界面注入自己的东西」，没有回边。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/resolve"
	"github.com/rzbdz/newgate/modules/config/roleprov"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/modules/gateway/controlplane"
)

// status 行/块的位置（Rank 小的在前）。配置排在代理与接管之后：它是「用哪个
// profile」，不是「有没有在跑」。
const (
	rankCheckFiles   = 10
	rankCheckChain   = 15
	rankStatusConfig = 30

	// 诊断包里的位置（见 cliapi.DumpSection）。
	rankDumpProviders = 20
	rankDumpBindings  = 30
	rankBlockBinding  = 100
	rankBlockChain    = 110
)

// reporter 同时是三种贡献者：体检、状态行、状态块。
//
// 合成一个类型是因为它们回答的都是「配置现在什么样」，拆成三个只会让
// module.go 里多注册两次。
type reporter struct{}

var (
	_ cliapi.DiagnosticProvider = reporter{}
	_ cliapi.Dumper             = reporter{}
	_ cliapi.StatusProvider     = reporter{}
	_ cliapi.BlockProvider      = reporter{}
)

// ---------- doctor ----------

func (reporter) Diagnostics() []cliapi.Diagnostic {
	return []cliapi.Diagnostic{checkFiles(), checkChain()}
}

// checkFiles 三个必需文件在不在。
func checkFiles() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckFiles, Label: "文件"}
	var missing, ok []string
	for _, p := range []string{paths.ProvidersFile(), paths.Mappings()} {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, filepath.Base(p))
		} else {
			ok = append(ok, filepath.Base(p))
		}
	}
	// state.json 单独查：**存在不够，它得读得出来**。半截 JSON 走 fail-open 会给
	// 一个零值 State（默认 profile 是空串、端口是 0），症状是「无可用候选」或
	// 「代理在跑却连不上」，而只 os.Stat 的体检会把那个文件列进 ok 里、报全部通过
	// ——用户拿到的是一组互相矛盾的现象（见 store.ValidateState 的说明）。
	if err := store.ValidateState(); err != nil {
		missing = append(missing, "state.json（"+err.Error()+"）")
	} else {
		ok = append(ok, "state.json")
	}
	extra := ""
	if names, err := store.ListProfiles(); err == nil {
		extra = fmt.Sprintf(" · %d 个 profile", len(names))
	}
	if len(missing) > 0 {
		d.State = "bad"
		d.Line = strings.Join(missing, " · ") + " 不存在"
		d.Details = append(d.Details, "newgate init 铺开默认配置")
		return d
	}
	d.State = "ok"
	d.Line = strings.Join(ok, " · ") + extra
	return d
}

// checkChain profile 引用的 provider 与 key 齐不齐。
func checkChain() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckChain, Label: "链路"}
	probs := store.Validate()
	names, _ := store.ListProfiles()
	if len(probs) == 0 {
		d.State = "ok"
		d.Line = fmt.Sprintf("%d 个 profile 的 provider 与 key 均可用", len(names))
		return d
	}
	d.State = "bad"
	d.Line = fmt.Sprintf("%d 处配置问题", len(probs))
	d.Details = probs
	return d
}

// ---------- status ----------

// Status 一行说清用哪个 profile，有 per-agent 覆盖才展开。
func (reporter) Status() []cliapi.StatusLine {
	st := store.LoadState()
	line := style.Cyan(st.DefaultProfile) + style.Dim(" 默认")
	over := 0
	var parts []string
	for _, id := range sortedAgentIDs(st) {
		if p := st.Active[id]; p != "" && p != st.DefaultProfile {
			parts = append(parts, id+" → "+style.Cyan(p))
			over++
		}
	}
	if over > 0 {
		line += "   " + strings.Join(parts, "   ")
	}
	return []cliapi.StatusLine{{Rank: rankStatusConfig, Label: "配置", Value: line}}
}

// sortedAgentIDs 稳定的已知 agent 顺序。
//
// 顺序取自 state.Active 的键排序而不是注册表：这一行只关心**有覆盖的** agent，
// 没覆盖的本来就不出现。注册表里那些（cli 那边的 AgentCatalog）在这一屏上用不上，
// 所以这里不必再去依赖那个端口。
func sortedAgentIDs(st *domain.State) []string {
	ids := make([]string, 0, len(st.Active))
	for id := range st.Active {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// ---------- status 的块 ----------

func (reporter) StatusBlocks() []cliapi.StatusBlock {
	st := store.LoadState()
	_, doc := controlplane.State()
	return []cliapi.StatusBlock{bindingBlock(st), chainBlock(st, doc)}
}

// bindingBlock 默认 profile 的档位绑定表。
func bindingBlock(st *domain.State) cliapi.StatusBlock {
	block := cliapi.StatusBlock{Rank: rankBlockBinding,
		Title: "档位绑定   profile " + st.DefaultProfile}
	pr, err := store.LoadProfile(st.DefaultProfile)
	if err != nil {
		block.Lines = append(block.Lines,
			style.Item(style.Warn, fmt.Sprintf("默认 profile %q 读不出: %v", st.DefaultProfile, err)))
		return block
	}
	provs, _ := store.LoadProviders()
	t := style.NewTable("档位", "绑定", "备注")
	for _, role := range domain.Roles {
		b, ok := pr.Resolve(role)
		if !ok {
			t.Row(style.Dim(role), style.Dim("未绑定"), "")
			continue
		}
		note := ""
		if provs != nil {
			if p, exists := provs.Providers[b.Provider]; !exists {
				note = style.Red("provider 未定义")
			} else if p.Key() == "" {
				note = style.Yellow("缺 api_key")
			}
		}
		t.Row(style.Cyan(role), b.String(), note)
	}
	block.Lines = append(block.Lines, strings.TrimRight(t.String(), "\n"))
	return block
}

// chainBlock normal 档的完整候选链。
func chainBlock(st *domain.State, doc *controlplane.Doc) cliapi.StatusBlock {
	block := cliapi.StatusBlock{Rank: rankBlockChain}
	snap, err := store.Load()
	if err != nil {
		return block
	}
	block.Title = "fallback 链   normal 档，按序尝试"
	// MaxSteps: 0——`status` 展示的是**完整候选链**，maxAttempts 是单次请求的
	// 执行上限，不是 membership。传 Attempts() 会把第 4 站之后静默裁掉，于是
	// 「normal 档里怎么没有 ark」这种观感问题（2026-09-17 实查：ark 是第 4 站，
	// 被这里截掉了）。截断的事实用下面那行提示说，而不是让它看起来像不在链里。
	steps, skips := resolve.BuildChain("normal", snap.Profiles, snap.Providers, resolve.Opts{
		Active: st.DefaultProfile, Available: doc.Available(), Rank: doc.Rank(),
		MaxSteps: 0})
	if len(steps) == 0 {
		block.Lines = append(block.Lines, style.Item(style.Warn, "无可用候选   newgate tier normal"))
		return block
	}
	block.Lines = append(block.Lines, strings.TrimRight(bindingChain(steps, "  "), "\n"))
	if limit := st.Chain.Attempts(); limit < len(steps) {
		block.Lines = append(block.Lines, style.Hint(fmt.Sprintf(
			"单次请求最多尝试前 %d 站；后续 %d 站仍在链中（newgate tier normal 看明细）",
			limit, len(steps)-limit)))
	}
	if tail := chainTail(steps, skips); tail != "" {
		block.Lines = append(block.Lines, style.Hint(tail))
	}
	return block
}

// chainTail 链尾的一句话总结：还有多少候选被跳过、去哪儿看原因。
func chainTail(steps []resolve.Step, skips []resolve.Skip) string {
	if len(skips) == 0 {
		return fmt.Sprintf("链上 %d 站", len(steps))
	}
	reasons := map[string]int{}
	for _, s := range skips {
		reasons[skipKind(s.Reason)]++
	}
	var parts []string
	for _, k := range skipKinds {
		if n := reasons[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	return fmt.Sprintf("链上 %d 站；跳过 %d（%s）   newgate tier normal",
		len(steps), len(skips), strings.Join(parts, " · "))
}

// ---------- 诊断包的原始素材 ----------

// Dump 交出配置自己的两段原文：providers.json（脱敏）与所有 profile 的绑定。
//
// 为什么是**脱敏**的：诊断包是要贴进 issue / 发给别人看的，key 只报长度与前缀。
func (reporter) Dump() []cliapi.DumpSection {
	return []cliapi.DumpSection{dumpProviders(), dumpBindings()}
}

func dumpProviders() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpProviders, Title: "providers.json（密钥脱敏）"}
	provs, err := store.LoadProviders()
	if err != nil {
		s.Lines = append(s.Lines, "  读不到: "+err.Error())
		return s
	}
	for _, n := range sortedKeys(provs.Providers) {
		p := provs.Providers[n]
		k := "(空)"
		if v := p.Key(); v != "" {
			if len(v) > 10 {
				k = v[:7] + "…" + strconv.Itoa(len(v)) + "字符"
			} else {
				k = "(过短)"
			}
		}
		s.Lines = append(s.Lines, fmt.Sprintf("  %-14s %-45s protocol=%-10s key=%s",
			n, p.BaseURL, p.Protocol, k))
		// 两种方言分家的上游：另一个 base 也报出来，否则「claude 的流量到底发去
		// 哪」在这一屏里是黑盒。
		if p.AnthropicURL != "" {
			s.Lines = append(s.Lines, fmt.Sprintf("  %-14s %-45s （anthropic 方言走这条）", "", p.AnthropicURL))
		}
	}
	return s
}

func dumpBindings() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpBindings, Title: "所有 profile 的绑定"}
	names, _ := store.ListProfiles()
	st := store.LoadState()
	for _, n := range names {
		mark := " "
		if n == st.DefaultProfile {
			mark = "*"
		}
		s.Lines = append(s.Lines, fmt.Sprintf(" %s %s", mark, n))
		pr, err := store.LoadProfile(n)
		if err != nil {
			continue
		}
		for _, role := range domain.Roles {
			if b, ok := pr.Resolve(role); ok {
				s.Lines = append(s.Lines, fmt.Sprintf("      %-8s %s/%s", role, b.Provider, b.Model))
			}
		}
	}
	return s
}

// sortedKeys 稳定顺序的 map 键。
func sortedKeys(m map[string]domain.Provider) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// glossary 贡献帮助屏术语表里的「槽位键」一行。
//
// 它是**动态角色**的词汇（模块贡献的键，写法同档位），所以归本模块。界面原来
// 为了这一行得认识 domain.ExtraRoles，还得记得先 Refresh 一次（见下面那段注释）
// ——那是本模块该操心的时序，不该由界面代管。
type glossary struct{}

var _ cliapi.Glossarist = glossary{}

func (glossary) Glossary() []cliapi.GlossaryLine {
	// 现刷一次动态角色表。它平时只在 store.Load（装配置快照）时刷新，而
	// `--help` 不装快照——不刷新的话这里读到的永远是空的，帮助里那行例子
	// 就会静默消失（2026-09-18 实测：改成现取之后例子没了）。读失败不挡
	// 帮助（失败开放），最差是那行退化成不带例子的说法。
	_ = roleprov.Refresh()

	var keys []string
	for _, r := range domain.ExtraRoles() {
		if r.Source == "builtin" {
			continue // 内置别名（normal→mid）是向下兼容，不是「槽位键」的例子
		}
		keys = append(keys, r.Key)
	}
	def := "模块贡献的动态角色，写法同档位"
	switch {
	case len(keys) > 2:
		keys = keys[:2]
		fallthrough
	case len(keys) > 0:
		def = "模块贡献的动态角色（" + strings.Join(keys, " / ") + "），写法同档位"
	}
	// profile 与配置文件的说法本来写死在界面里（cli.usageText），可这两样都是
	// **本模块的词汇**：界面凭什么知道 providers.json 叫什么、state.json 在哪。
	// 2026-09-18 搬回来之后，界面那一节只剩「从账本里读行」这一件事。
	//
	// 目录与文件名**分两行**：合成一行会有 78 列，超过版面上限 75（style.MaxColumns）
	// ——而 `style.Wrap` 是按显示宽度切字符的，不认词边界，于是它会从 `state.json`
	// 中间断开成 `stat` / `e.json`，看着像个错字。这里宁可行数多一行。
	// 目录现取 paths.Config()：沙箱（NEWGATE_HOME）与 --home 覆盖下真正生效的
	// 是哪一个，只有这里说了算。
	return []cliapi.GlossaryLine{
		{Rank: 15, Term: "profile", Definition: "一套「档位 → provider/模型」绑定"},
		{Rank: 20, Term: "槽位键", Definition: def},
		{Rank: 30, Term: "配置目录", Definition: paths.Config()},
		{Rank: 31, Term: "配置文件", Definition: "providers.json · mappings/*.kv · state.json"},
	}
}
