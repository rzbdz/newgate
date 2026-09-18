package config

// 本文件是**配置自己的**那批命令：tier / profiles / profile kv / --set-profile /
// agents。2026-09-18 从 modules/cli/profile.go 整体搬来。
//
// 为什么搬：档位怎么解析、链怎么建、跳过谁、profile 文件长什么样——全是配置的
// 知识，界面留着一份「抄来的理解」就必然和实现漂移（这一轮之前，界面为了渲染
// tier 得 import resolve + domain + paths + store）。搬回来之后界面既不认识
// profile，也不认识链。
//
// 依赖方向：config → cli/extension（叶子契约），不是 → modules/cli。命令由本模块
// 在 Start 里注入界面，和其它模块完全一样。

import (
	"fmt"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/lib/style"
	"github.com/rzbdz/newgate/go/modules/breaker/status"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"

	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// cmdSetProfile switches the chain head. An empty agent sets the global
// default; naming an agent switches only that agent, leaving every other
// agent — including ones with sessions in flight — untouched.
func cmdSetProfile(agent, name string) int {
	if err := store.SetActiveProfile(agent, name); err != nil {
		return style.Die(65, err.Error())
	}
	scope := "全局默认"
	if agent != "" {
		scope = "agent " + agent
	}
	fmt.Println(style.Item(style.OK, scope+" → "+style.Cyan(name)))

	if pr, err := store.LoadProfile(name); err == nil {
		if pr.Description != "" {
			fmt.Println(style.Hint(pr.Description))
		}
		if pr.Pinned {
			fmt.Println(style.Hint("pinned：链到此为止，失败直接上报，不再替换候选"))
		}
		t := style.NewTable("档位", "绑定")
		for _, tier := range domain.Roles {
			if b, ok := pr.Resolve(tier); ok {
				t.Row(style.Cyan(tier), b.String())
			}
		}
		if t.Len() > 0 {
			fmt.Println()
			fmt.Print(t.String())
		}
	}
	controlplane.Notify()
	fmt.Println()
	if daemon.Running() != nil {
		fmt.Println(style.Hint("即刻生效；已在运行的会话不受影响"))
	} else {
		fmt.Println(style.Hint("代理未运行 · newgate start"))
	}
	return 0
}

// cmdProfiles 列出全部 profile。
//
// 一屏回答三个问题：默认是哪个、优先级怎么排、哪个 agent 挂了别名的 profile。
// 标了标志的 profile 才是异常的（pinned / excluded），所以不加额外段落，
// 全部信息压在一张表里——段落一多，扫读就变成阅读。
func cmdProfiles() int {
	names, err := store.ListProfiles()
	if err != nil {
		return style.Die(65, "cannot read mappings: "+err.Error())
	}
	st := store.LoadState()
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := store.LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Prio() != ps[j].Prio() {
			return ps[i].Prio() < ps[j].Prio()
		}
		return ps[i].Name < ps[j].Name
	})

	fmt.Println(style.Title("newgate profiles",
		fmt.Sprintf("%d 个 · 默认 %s", len(ps), st.DefaultProfile)))
	fmt.Println(style.Rule(72))

	t := style.NewTable("优先级", "profile", "标志", "说明")
	t.AlignRight(0)
	for _, p := range ps {
		var flags []string
		if p.Name == st.DefaultProfile {
			flags = append(flags, style.Green("default"))
		}
		if p.Pinned {
			flags = append(flags, style.Yellow("pinned"))
		}
		if p.Excluded {
			flags = append(flags, style.Yellow("excluded"))
		}
		for agent, ap := range st.Active {
			if ap == p.Name {
				flags = append(flags, style.Cyan("←"+agent))
			}
		}
		name := p.Name
		if p.Name == st.DefaultProfile {
			name = style.Cyan(p.Name)
		}
		t.Row(fmt.Sprintf("%d", p.Prio()), name, strings.Join(flags, " "),
			style.Dim(p.Description))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint("pinned 停在链首不替换 · excluded 只能被显式选中 · ←agent 该 agent 单独用这个 profile"))
	return 0
}

// cmdProfileKV 把一个 profile 转成 KV 文本——json→kv 的转换口。
//
// 不带 --write 只打印（拷走、改完再贴回来都行）；带 --write 落盘成
// mappings/<名>.kv 并把旧 .json 改名 .bak（kv 优先于 json，留着旧文件
// 会永远被压着，退役比并存干净）。
func cmdProfileKV(args []string) int {
	if len(args) < 1 || args[0] == "" {
		return style.Die(64, "用法：newgate profile kv <名> [--write]")
	}
	name := args[0]
	raw, err := store.LoadProfileRaw(name)
	if err != nil {
		return style.Die(65, err.Error())
	}
	text := store.SerializeProfileKV(raw)

	if len(args) < 2 || args[1] != "--write" {
		fmt.Print(text)
		fmt.Println(style.Dim("# 落盘：newgate profile kv " + name + " --write"))
		return 0
	}
	kvPath := filepath.Join(paths.Mappings(), name+".kv")
	if err := os.WriteFile(kvPath, []byte(text), 0o660); err != nil {
		return style.Die(70, "写 "+kvPath+" 失败: "+err.Error())
	}
	jsonPath := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(jsonPath); err == nil {
		if err := os.Rename(jsonPath, jsonPath+".bak"); err != nil {
			return style.Die(70, "旧 json 改名失败（kv 已写入，手动处理）: "+err.Error())
		}
		fmt.Println(style.Item(style.OK, kvPath+style.Dim("   旧 .json → .json.bak")))
	} else {
		fmt.Println(style.Item(style.OK, kvPath))
	}
	controlplane.Notify()
	return 0
}

// tierView 一个档位解析出来的结果。
type tierView struct {
	name  string
	steps []resolve.Step
	skips []resolve.Skip
}

// skipKinds 类目顺序固定：数字对不上时，两次输出可以直接比。
var skipKinds = []string{"excluded", "未定义", "没 key", "熔断", "已禁用",
	"超出 maxAttempts", "去重", "引用成环", "其他"}

// cmdTier 展示 fallback 链——这是整套配置的**接口**：一眼要能回答
// 「这次请求会走谁」和「为什么不是我想的那个」。
//
// 版式分两档（详略得当）：
//
//	newgate tier            每个档位一行，链相同就写 `= heavy`，末行给跳过统计
//	newgate tier <档位>     展开：编号的站 + 按原因分组的跳过统计
//
// 跳过一律**只给汇总**，永不逐条铺开。实测一份配置里 106 条跳过中有 91 条
// 是「与链上更靠前的候选重复」——把必然发生的去重当成一行行结果打出来，
// 只会把真正的结论（走谁）淹掉。用户要的是链，不是候选全集的流水账。
func cmdTier(args []string) int {
	which := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		which = a
	}
	// 缩写也认（heavy 写成 h、normal 写成 n…），省得记全名。
	// 校验放在 tierReport 里、store.Load 之后——动态角色键要等 Load 把
	// roleprov 刷新过才认得（在这里查会把自己刚注册的键判成未知）。
	return tierReport(which)
}

// knownRolesLine 报错时给的可选项。动态角色键可能几十个，全列出来会把
// 错误信息冲成一堵墙，只给数量与查询入口。
func knownRolesLine() string {
	if n := len(domain.ExtraRoles()); n > 0 {
		return fmt.Sprintf("档位 %s，另有 %d 个动态角色键（newgate omo ls）",
			strings.Join(domain.Roles, "/"), n)
	}
	return "档位 " + strings.Join(domain.Roles, "/")
}

func matchTier(s string) string {
	for _, r := range domain.Roles {
		if strings.HasPrefix(r, s) {
			return r
		}
	}
	return ""
}

func tierReport(which string) int {
	snap, err := store.Load()
	if err != nil {
		return style.Die(65, err.Error())
	}
	st := snap.State

	// 角色键的校验必须在这里做：动态角色键（omo-sisyphus / cat-deep）是
	// store.Load 里跟着 roleprov 刷进来的，在 Load 之前查会把自己刚注册
	// 的键判成未知。模块注册的槽位和档位在解析层是一回事，命令层不该比
	// 解析层更窄——newgate tier omo-sisyphus 是查「这个槽位走哪条链」的
	// 正规入口。
	if which != "" {
		if full := matchTier(which); full != "" {
			which = full
		} else if !domain.IsKnownRole(which) {
			return style.Die(64, fmt.Sprintf("未知档位 %q（%s）", which, knownRolesLine()))
		}
	}

	// 链头可能不止一个（claude 和 opencode 可以各挂一个 profile）。
	heads := map[string][]string{st.DefaultProfile: {"默认"}}
	for agent, p := range st.Active {
		heads[p] = append(heads[p], agent)
	}
	var headNames []string
	for h := range heads {
		headNames = append(headNames, h)
	}
	sort.Strings(headNames)

	names := domain.Roles
	if which != "" {
		names = []string{which}
	}
	_, live := controlplane.State()
	available := live.Available()
	rank := live.Rank()
	liveHealth := healthFromProxy(live)

	for _, head := range headNames {
		sort.Strings(heads[head])
		fmt.Println(style.Title("newgate tier",
			fmt.Sprintf("链头 %s（%s）· 最大尝试 %d · 预算 %s",
				head, strings.Join(heads[head], ", "),
				st.Chain.Attempts(), prettyMs(st.Chain.Budget()))))
		fmt.Println(style.Rule(72))

		var rows []tierView
		for _, name := range names {
			steps, skips := resolve.BuildChain(name, snap.Profiles, snap.Providers, resolve.Opts{
				Active:    head,
				Available: available,
				Rank:      rank,
				// tier 展示的是完整候选链；maxAttempts 是执行约束，不是
				// membership。否则后续 profile 会被误解成根本没进链。
				MaxSteps: 0,
			})
			rows = append(rows, tierView{name, steps, skips})
		}

		fmt.Println()
		if which == "" {
			// 概览：链一样的档位合并成 `= <先出现的那个>`
			fmt.Print(tierOverview(rows))
			if n := countSkips(rows); n > 0 {
				fmt.Println(style.Hint(fmt.Sprintf("跳过 %d 个候选：%s", n, skipSummary(rows))))
				fmt.Println(style.Hint("明细：newgate tier <档位>"))
			}
			continue
		}

		r := rows[0]
		if len(r.steps) == 0 {
			fmt.Println(style.Item(style.Bad, "无可用候选"))
		} else {
			fmt.Println(style.Field("最终", style.Cyan(r.steps[0].Binding.String())))
			fmt.Println()
			fmt.Print(numberedBindingChain(r.steps, func(step resolve.Step) string {
				return bindingHealthLabel(liveHealth[step.Binding.String()])
			}))
			fmt.Println(style.Hint("链头固定；fallback 按当前上下文的预测 TTFT 排序"))
			fmt.Println(style.Hint("同 (provider, model) 全链仅一次"))
			if limit := st.Chain.Attempts(); limit < len(r.steps) {
				fmt.Println(style.Hint(fmt.Sprintf(
					"单次请求最多尝试前 %d 站；后续 %d 站仍在链中",
					limit, len(r.steps)-limit)))
				fmt.Println(style.Hint("调高 state.json chain.max_attempts 可扩大实际尝试范围"))
			}
		}
		if len(r.skips) > 0 {
			fmt.Println()
			printSkips(r.skips)
		}
	}
	return 0
}

// bindingHealthLabel 一行说清这条 binding 现在什么状态。
//
// **阈值与档位名都来自 breaker/status**（2026-09-18）：这里曾经自己抄了一份
// 3000/12000，于是 daemon 改口径这一屏不会跟着变。
func bindingHealthLabel(h status.Status) string {
	if h.Open {
		return style.Red("熔断")
	}
	grade := status.Grade(h.ScoreMs, h.ScoreMs > 0)
	switch {
	case grade == status.LatencyUnknown && h.Grade == status.ProbeUnavailable:
		return style.Red("不可用")
	case grade == status.LatencyUnknown:
		return style.Dim("未探")
	}
	label := fmt.Sprintf("%s %dms", grade, h.ScoreMs)
	switch grade {
	case status.LatencyFast:
		return style.Green(label)
	case status.LatencyOK:
		return style.Yellow(label)
	default:
		return style.Red(label)
	}
}

func chainSig(steps []resolve.Step) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Binding.String())
		b.WriteByte('|')
	}
	return b.String()
}

func bindingChain(steps []resolve.Step, indent string) string {
	var out strings.Builder
	for i, step := range steps {
		out.WriteString(indent)
		if i > 0 {
			out.WriteString(style.Dim("→ "))
		}
		out.WriteString(step.Binding.String())
		out.WriteByte('\n')
	}
	return out.String()
}

// PrintChain 把编号后的候选链渲染出来，理由同 PrintSkips。
func PrintChain(steps []resolve.Step) { fmt.Print(numberedBindingChain(steps, nil)) }

func numberedBindingChain(steps []resolve.Step, extra func(resolve.Step) string) string {
	var out strings.Builder
	for i, step := range steps {
		out.WriteString(fmt.Sprintf("  %d. %s\n", i+1, step.Binding.String()))
		detail := "profile " + step.Profile
		if extra != nil {
			if value := extra(step); value != "" {
				detail += " · " + value
			}
		}
		out.WriteString(style.Hint(detail))
		out.WriteByte('\n')
	}
	return out.String()
}

func tierOverview(rows []tierView) string {
	var out strings.Builder
	out.WriteString("  " + style.Dim(style.Pad("档位", 6)) + "  " + style.Dim("链") + "\n")
	firstOf := map[string]string{}
	for _, row := range rows {
		prefix := "  " + style.Cyan(style.Pad(row.name, 6)) + "  "
		switch {
		case len(row.steps) == 0:
			out.WriteString(prefix + style.Red("无可用候选") + "\n")
		case firstOf[chainSig(row.steps)] != "":
			out.WriteString(prefix + style.Dim("= "+firstOf[chainSig(row.steps)]) + "\n")
		default:
			firstOf[chainSig(row.steps)] = row.name
			for i, step := range row.steps {
				if i == 0 {
					out.WriteString(prefix + step.Binding.String() + "\n")
				} else {
					out.WriteString("          " + style.Dim("→ ") + step.Binding.String() + "\n")
				}
			}
		}
	}
	return out.String()
}

func countSkips(rows []tierView) int {
	n := 0
	for _, r := range rows {
		n += len(r.skips)
	}
	return n
}

// skipSummary 跳过原因按类目计数。用户真正想问的是「为什么没轮到它」，
// 一百行里其实只有三五类原因。
func skipSummary(rows []tierView) string {
	reasons := map[string]int{}
	for _, r := range rows {
		for _, s := range r.skips {
			reasons[skipKind(s.Reason)]++
		}
	}
	var parts []string
	for _, k := range skipKinds {
		if n := reasons[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	return strings.Join(parts, " · ")
}

// printSkips 跳过**汇总**。永远只给按原因分组的计数与一句解释——
// 逐条铺开是候选全集的流水账，真正的结论是上面那条链。
//
// 需要一个个看的时候有别的口子：newgate profiles 看标志、doctor 看链路、
// metrics / probe 看熔断，那些才是可操作的信息。
// skipKind 把 skip 的自由文本归成几个可数的类目。
func skipKind(reason string) string {
	switch {
	case strings.Contains(reason, "excluded"):
		return "excluded"
	case strings.Contains(reason, "熔断"):
		return "熔断"
	case strings.Contains(reason, "maxAttempts"):
		return "超出 maxAttempts"
	case strings.Contains(reason, "重复") || strings.Contains(reason, "去重"):
		return "去重"
	case strings.Contains(reason, "未定义"):
		return "未定义"
	case strings.Contains(reason, "api_key"):
		return "没 key"
	case strings.Contains(reason, "已禁用"):
		return "已禁用"
	case strings.Contains(reason, "成环"):
		return "引用成环"
	}
	return "其他"
}

// healthFromProxy 把 daemon 的熔断表按 "provider/model" 索引成一次命令内的快照。
// daemon 不在线时是空表——诊断退化为只看静态配置，不凭空判坏。
func healthFromProxy(ps *controlplane.Doc) map[string]status.Status {
	out := map[string]status.Status{}
	if ps != nil {
		for _, s := range ps.Breakers {
			out[s.Provider+"/"+s.Model] = s
		}
	}
	return out
}

// prettyMs 毫秒 → 人话。链预算是按 ms 配的（state.json 里 120000），
// 打印时不该原样甩 120000ms 给用户。
func prettyMs(ms int) string { return (time.Duration(ms) * time.Millisecond).String() }

// PrintSkips 把跳过汇总渲染出来。导出的理由：**需要它的模块命令**（opencodeomo
// 的 `newgate omo`）直接调它，排版归本模块。
//
// 它曾经挂在界面的 `Host` 上（界面替模块转发），2026-09-18 摘掉了——转发让界面
// import 配置，而界面不该认识任何模块。
func PrintSkips(skips []resolve.Skip) { printSkips(skips) }

func printSkips(skips []resolve.Skip) {
	reasons := map[string][]resolve.Skip{}
	for _, s := range skips {
		k := skipKind(s.Reason)
		reasons[k] = append(reasons[k], s)
	}
	fmt.Println(style.Item(style.Skip, fmt.Sprintf("%d 个候选被跳过", len(skips))))

	t := style.NewTable("原因", "数量", "说明")
	t.AlignRight(1)
	for _, k := range skipKinds {
		group := reasons[k]
		if len(group) == 0 {
			continue
		}
		t.Row(k, fmt.Sprintf("%d", len(group)), style.Dim(skipDetail(group[0])))
	}
	fmt.Print(t.String())
}

// skipDetail 给整组配一句「所以呢」——光有类目名，用户还是不知道要改什么。
func skipDetail(s resolve.Skip) string {
	switch skipKind(s.Reason) {
	case "excluded":
		return "excluded profile 只能被显式选中，不参与自动排序"
	case "未定义":
		return "该 profile 未定义此档位（稀疏层，正常）"
	case "没 key":
		return "provider 缺 api_key"
	case "熔断":
		return "provider 被熔断摘除，见 newgate metrics / probe"
	case "已禁用":
		return s.Reason
	case "超出 maxAttempts":
		return "链的尝试次数已用尽，见 state.json chain.max_attempts"
	case "去重":
		return "与链上更靠前的候选重复"
	case "引用成环":
		return s.Reason
	}
	return s.Reason
}

// ---------- 配置自己的两条维护命令 ----------

type initCommand struct{}

func (initCommand) Names() []string { return []string{"init"} }

func (initCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "init [--force]", Summary: "铺开默认配置"}
}

func (initCommand) Run(_ cliapi.Host, args []string) int { return runInit(hasFlag(args, "--force")) }

// runInit 铺开默认配置。它归 config：写的是 providers.json / state.json / mappings，
// 全是本模块的文件。
func runInit(force bool) int {
	created, err := store.Init(force)
	if err != nil {
		return style.Die(70, err.Error())
	}
	if len(created) == 0 {
		fmt.Println("配置已存在，无需初始化（--force 可覆盖）")
	} else {
		for _, c := range created {
			fmt.Println("创建 " + c)
		}
	}
	fmt.Printf("\n下一步：把上游 key 填进 %s\n", paths.ProvidersFile())
	fmt.Println("默认写入的是占位符，必须改成你自己的 provider / endpoint / 模型名。")
	fmt.Println("key 建议走环境变量（不落盘）：NEWGATE_KEY_<PROVIDER 大写，- 换 _>")
	return 0
}

type reloadCommand struct{}

func (reloadCommand) Names() []string { return []string{"reload"} }

func (reloadCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "reload", Summary: "立刻重读配置（平时 1 秒内自动热更新）"}
}

func (reloadCommand) Run(_ cliapi.Host, _ []string) int {
	info, _ := controlplane.State()
	if info == nil {
		fmt.Println(style.Item(style.Skip, "代理未运行；配置会在下次启动时读取"))
		return 0
	}
	controlplane.Notify()
	fmt.Println(style.Item(style.OK, fmt.Sprintf("已通知代理重读配置   pid %d", info.PID)))
	fmt.Println(style.Hint("平时不需要这个命令：配置改动 1 秒内自动生效"))
	return 0
}

// ---------- 命令声明 ----------
//
// 每一条都只做「解析 argv → 调本文件里的实现」：帮助行、位置、别名是**给界面看
// 的元数据**，由拥有这条命令的模块声明（见 cliapi.Documented）。

type tierCommand struct{}

var (
	_ cliapi.Command    = (*tierCommand)(nil)
	_ cliapi.Documented = (*tierCommand)(nil)
)

func (tierCommand) Names() []string { return []string{"tier", "tiers", "role", "roles"} }

func (tierCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "tier [档位]", Summary: "fallback 链：走谁、跳过了什么"}
}

func (tierCommand) Run(_ cliapi.Host, args []string) int { return cmdTier(args) }

type profilesCommand struct{}

var (
	_ cliapi.Command    = (*profilesCommand)(nil)
	_ cliapi.Documented = (*profilesCommand)(nil)
)

func (profilesCommand) Names() []string { return []string{"profiles", "ls"} }

func (profilesCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "profiles", Summary: "所有 profile（优先级 / 标志 / 覆盖）"}
}

func (profilesCommand) Run(_ cliapi.Host, _ []string) int { return cmdProfiles() }

type profileCommand struct{}

var (
	_ cliapi.Command    = (*profileCommand)(nil)
	_ cliapi.Documented = (*profileCommand)(nil)
	_ cliapi.Unstyled   = (*profileCommand)(nil)
)

func (profileCommand) Names() []string { return []string{"profile"} }

// Unstyled：`profile kv` 吐的是给管道/文件用的原始 KV 文本，不是控制面版式。
func (profileCommand) Unstyled(args []string) bool { return cliapi.Arg(args, 0) == "kv" }

func (profileCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "profile kv <名> [--write]", Summary: "profile 转 KV 文本"}
}

func (profileCommand) Run(host cliapi.Host, args []string) int {
	if cliapi.Arg(args, 0) == "kv" {
		return cmdProfileKV(args[1:])
	}
	return host.Die(64, "用法：newgate profile kv <名> [--write]")
}

type setProfileCommand struct{}

var (
	_ cliapi.Command    = (*setProfileCommand)(nil)
	_ cliapi.Documented = (*setProfileCommand)(nil)
)

func (setProfileCommand) Names() []string { return []string{"--set-profile"} }

func (setProfileCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage:   "--set-profile <名> [--agent <agent>]",
		Summary: "切 profile；省略 --agent 设全局默认"}
}

// Run 的形状由界面归一后给：args[0] 是 profile 名，可选的 `--agent <agent>` 跟在
// 后面。界面负责认 `--set-profile=x` 与 `--set-profile x` 两种写法（argv 解析是它
// 的活），语义归本模块。
func (setProfileCommand) Run(_ cliapi.Host, args []string) int {
	name := cliapi.Arg(args, 0)
	if name == "" {
		return style.Die(64, "--set-profile 需要一个 profile 名")
	}
	return cmdSetProfile(flagValue(args, "--agent"), name)
}

// flagValue 取 `--名字 值` 里的值，没有给空串。
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// commands 是本模块注入界面的全部命令。没有它就等于没有入口——而入口是 ui 的事，
// 本模块的功能不受影响（见 CLAUDE.md §4）。
func commands() []cliapi.Command {
	return []cliapi.Command{
		tierCommand{}, profilesCommand{}, profileCommand{}, setProfileCommand{},
		initCommand{}, reloadCommand{},
	}
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}
