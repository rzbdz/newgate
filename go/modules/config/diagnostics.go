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
	"strings"

	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"
)

// status 行/块的位置（Rank 小的在前）。配置排在代理与接管之后：它是「用哪个
// profile」，不是「有没有在跑」。
const (
	rankCheckFiles   = 10
	rankCheckChain   = 15
	rankStatusConfig = 30
	rankBlockBinding = 100
	rankBlockChain   = 110
)

// reporter 同时是三种贡献者：体检、状态行、状态块。
//
// 合成一个类型是因为它们回答的都是「配置现在什么样」，拆成三个只会让
// module.go 里多注册两次。
type reporter struct{}

var (
	_ cliapi.DiagnosticProvider = reporter{}
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
	for _, p := range []string{paths.ProvidersFile(), paths.StateFile(), paths.Mappings()} {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, filepath.Base(p))
		} else {
			ok = append(ok, filepath.Base(p))
		}
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
	info, doc := controlplane.State()
	return []cliapi.StatusBlock{bindingBlock(st), chainBlock(st, info, doc)}
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
func chainBlock(st *domain.State, info *controlplane.Info, doc *controlplane.Doc) cliapi.StatusBlock {
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
	_ = info
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
