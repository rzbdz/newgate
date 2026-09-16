package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/health"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// cmdSetProfile switches the chain head. An empty agent sets the global
// default; naming an agent switches only that agent, leaving every other
// agent — including ones with sessions in flight — untouched.
func cmdSetProfile(agent, name string) int {
	if err := store.SetActiveProfile(agent, name); err != nil {
		return die(65, err.Error())
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
	notifyProxy()
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
		return die(65, "cannot read mappings: "+err.Error())
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
		return die(64, "用法：newgate profile kv <名> [--write]")
	}
	name := args[0]
	raw, err := store.LoadProfileRaw(name)
	if err != nil {
		return die(65, err.Error())
	}
	text := store.SerializeProfileKV(raw)

	if len(args) < 2 || args[1] != "--write" {
		fmt.Print(text)
		fmt.Println(style.Dim("# 落盘：newgate profile kv " + name + " --write"))
		return 0
	}
	kvPath := filepath.Join(paths.Mappings(), name+".kv")
	if err := os.WriteFile(kvPath, []byte(text), 0o660); err != nil {
		return die(70, "写 "+kvPath+" 失败: "+err.Error())
	}
	jsonPath := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(jsonPath); err == nil {
		if err := os.Rename(jsonPath, jsonPath+".bak"); err != nil {
			return die(70, "旧 json 改名失败（kv 已写入，手动处理）: "+err.Error())
		}
		fmt.Println(style.Item(style.OK, kvPath+style.Dim("   旧 .json → .json.bak")))
	} else {
		fmt.Println(style.Item(style.OK, kvPath))
	}
	notifyProxy()
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
		return die(65, err.Error())
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
			return die(64, fmt.Sprintf("未知档位 %q（%s）", which, knownRolesLine()))
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
	_, live := proxyState()
	available := availableFromProxy(live)
	rank := rankFromProxy(live)
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

func bindingHealthLabel(h health.Status) string {
	if h.Open {
		return style.Red("熔断")
	}
	switch {
	case h.ScoreMs > 0 && h.ScoreMs < 3000:
		return style.Green(fmt.Sprintf("流畅 %dms", h.ScoreMs))
	case h.ScoreMs >= 3000 && h.ScoreMs <= 12000:
		return style.Yellow(fmt.Sprintf("可用 %dms", h.ScoreMs))
	case h.ScoreMs > 12000:
		return style.Red(fmt.Sprintf("卡顿 %dms", h.ScoreMs))
	case h.Grade == health.ProbeUnavailable:
		return style.Red("不可用")
	default:
		return style.Dim("未探")
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

// cmdAgents 已知 agent 及其模型槽位。
//
// 每个 agent 一个小节：头一行是身份（方言 + 当前 profile），槽位一张表。
// 以前是一张通铺大表，说明列又长，中文一撑就错位。
func cmdAgents() int {
	st := store.LoadState()
	names := agentCatalog().Names()
	sort.Strings(names)

	fmt.Println(style.Title("newgate agents", fmt.Sprintf("%d 个", len(names))))
	fmt.Println(style.Rule(72))

	for _, n := range names {
		a, _ := agentCatalog().Get(n)
		profile := st.ActiveFor(a.ID)
		if profile == "" {
			profile = st.DefaultProfile
		}
		fmt.Println()
		fmt.Println("  " + style.Bold(a.ID) +
			style.Dim("   "+a.Dialect+" 方言") +
			style.Dim("   profile ") + style.Cyan(profile))
		if a.Notes != "" {
			fmt.Println(style.Hint(a.Notes))
		}
		if len(a.Slots) == 0 {
			fmt.Println(style.Hint("槽位在启动时从配置文件发现"))
			continue
		}
		t := style.NewTable("槽位", "档位", "环境变量")
		for _, s := range a.Slots {
			t.Row(s.Name, style.Cyan(s.Tier), s.EnvVar)
		}
		fmt.Print(t.String())
		// 说明单独一行：塞进表格会把整张表撑到一百多列，反而没法对读。
		for _, s := range a.Slots {
			if s.Desc != "" {
				fmt.Println(style.Hint(s.Name + "  " + s.Desc))
			}
		}
	}

	fmt.Println()
	fmt.Println(style.Hint("只切单个 agent：newgate --set-profile <名> --agent <agent>"))
	return 0
}
