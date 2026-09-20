package confighook

import (
	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 本文件是**一个客户端「从哪条链起步」的那一格**。
//
// # 它为什么是机制
//
// 链头（profile）本来只有全局一个，per-agent 那张表（`state.json` 的 `active`，
// 见 domain.State.Active）从 M1 起就在——`claude` 用贵的、`opencode` 用便宜的，
// 各切各的。但那条路一直**只有 CLI 有**（`newgate --set-profile <名> --agent <x>`），
// 界面上一个字都看不到：用户在浏览器里能改这个客户端的每一个槽位走哪一档，却改不了
// 「它整条链从哪儿开始」——而后者更靠前，它是那些档位最终落到哪家模型上的前提。
//
// 每一家客户端都要这一格（claude / codex / opencode），所以它住内核：留在某一个
// 模块里，另外两家只能各抄一遍，而里面有两条规矩是**抄错了不会红**的：
// 「空 = 跟随全局缺省」（而不是一份写死的 profile 名），以及写之前要验那个 profile
// 真的存在（打错一个名字的后果是这个客户端的每一次请求都起不来）。

// AgentProfile 是某一个客户端的链头。
type AgentProfile struct {
	// AgentID 是这个客户端的 id（`claude` / `codex` / …）。
	AgentID string
}

// Active 是这个客户端此刻走的 profile。
//
// **它总是有一个值**：没单独设过时返回全局默认——界面要显示的是「此刻实际走哪条
// 链」，而不是「用户有没有单独设过」。两者不一样，后者是 IsOverride。
func (p AgentProfile) Active() string {
	return store.LoadState().ActiveFor(p.AgentID)
}

// IsOverride 说这个客户端是不是**单独设过**（而不是跟着全局默认走）。
//
// 界面拿它说一句「跟随全局缺省」：不说的话，用户看到下拉里有一个值，会以为那是
// 自己设的——而他改全局默认时发现这个客户端没跟着变，就会以为哪儿坏了。
func (p AgentProfile) IsOverride() bool {
	st := store.LoadState()
	v, ok := st.Active[p.AgentID]
	return ok && v != "" && v != st.DefaultProfile
}

// Options 是可选的 profile 名。
//
// 读不出来时返回空表（没有 mappings 目录、目录读不了）：界面拿到一个空下拉，
// 而不是一个报错的卡片——一个读不了的目录不该让整张卡消失（同 lib/view 里
// Concept.Broken 那条：坏消息要显示出来，不是把东西藏起来）。
func (p AgentProfile) Options() []string {
	names, err := store.ListProfiles()
	if err != nil {
		return nil
	}
	return names
}

// Write 改这个客户端的链头。**空串 = 回到全局默认**（那个键消失）。
//
// 与槽位表那条同规矩：留下一份「和缺省一样」的记录，会让以后改缺省的人发现自己的
// 改动对一部分用户不生效——而那些用户从没配过任何东西。
func (p AgentProfile) Write(profile string) error {
	// **与全局默认同名 = 没设过**：界面上的取值来自 Active()（此刻实际走哪条），
	// 所以用户完全可能选中一个恰好等于全局默认的名字。那时留下一份「和缺省一样」的
	// 记录，会让以后改默认的人发现这个客户端没跟着变，而他从没单独设过它——
	// 与槽位表那边同一条规矩。
	if profile == "" || profile == store.LoadState().DefaultProfile {
		return store.ClearActiveProfile(p.AgentID)
	}
	return store.SetActiveProfile(p.AgentID, profile)
}

// Note 是给界面的一句话：这个客户端此刻是跟着全局默认，还是单独设过。
func (p AgentProfile) Note() string {
	if p.IsOverride() {
		return i18n.T("set for this client only (other clients follow the global default)", nil)
	}
	return i18n.T("following the global default ({profile})", i18n.A{"profile": p.Active()})
}

// ProfileItemID 是那一格在界面载荷里的键（形状见 view.go 的 ProfileItem）。
//
// 它必须与槽位名**不可能**撞上：界面一次交全部格子（见 kinds/Toggles.svelte），
// 而槽位表那边是按名字取值的——撞上就会把一个槽位的档位当成 profile 写下去。
// 槽位名是客户端自己的模型键（`model` / `sonnet`），所以一个带前缀的词就够。
const ProfileItemID = "profile"
