package runtime

// 本文件是**接管自己的**那批命令：start / stop / on / off / restart / shim。
// 2026-09-18 从 modules/cli/{lifecycle,shim}.go 整体搬来。
//
// 为什么搬：接管是 runtime 的语义——插上 shim、改写客户端配置、备份原文件、
// 释放回去，全是本模块在做的事。它们留在界面时，界面得认识 takeover / injection
// / confighook 三套东西才写得出来，而它真正该做的只有「把 argv 交给对的人」。
//
// 命令由本模块在 Start 里注册进界面：本模块对 ui 声明一条**弱依赖**
// （Optional(cli)，见 component.Optional），于是它排在界面之后——Start 跑到这里
// 时界面的账本已经就绪；没装界面就跳过，接管照常工作。

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/lib/httpx"
	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/store"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
	"github.com/rzbdz/newgate/go/modules/gateway/controlpath"
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
	"github.com/rzbdz/newgate/go/modules/runtime/injection"
	"github.com/rzbdz/newgate/go/modules/runtime/takeover"
)

// die 是统一的报错出口（版式在 lib/style）。
func die(code int, msg string) int { return style.Die(code, msg) }

// pingProxy 端口上的控制面活着吗。
func pingProxy(port int) bool { return controlplane.Ping(port) }

// cmdShim 是**底层逃生口**，不是日常命令。
//
// 日常接管请用 newgate on/off <agent>：用户不该被迫知道自己的工具是靠
// PATH shim 接管还是靠改配置文件接管——那是 runtime/takeover 的事
// （docs/08-operations.md：逃生口是一等功能，但它得摆在逃生口的位置上）。
//
// 这里只留三件 takeover 层不做的事：
//   - status：把 shim 目录的真实情况摊开，排查「装了却没生效」
//   - uninstall：连 rc 里那行 PATH 一起删干净（takeover 只摘链接，
//     留着空目录在 PATH 里是无害的，也让下次 on 不用再动 rc）
//   - install/off：老命令的别名，直接转给 on/off，免得肌肉记忆报错
func cmdShim(agents confighookapi.AgentCatalog, sub, which string) int {
	switch sub {
	case "install", "add", "on":
		if which == "" {
			return die(64, "用法：newgate shim install <agent>（已知："+
				strings.Join(agents.Names(), ", ")+"）")
		}
		fmt.Println(style.Hint("直接用 newgate on " + which + " 即可，机制由 newgate 自行选择"))
		fmt.Println()
		return cmdTakeover(agents, which)

	case "off", "remove", "rm":
		if which == "" {
			return die(64, "用法：newgate shim off <agent>（已知："+
				strings.Join(agents.Names(), ", ")+"）")
		}
		fmt.Println(style.Hint("直接用 newgate off " + which + " 即可"))
		fmt.Println()
		return cmdRelease(which)

	case "uninstall", "purge":
		for _, n := range injection.Installed() {
			if err := injection.Uninstall(n); err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, n+": "+err.Error()))
				continue
			}
			fmt.Println(style.Item(style.OK, "摘掉 shim "+n))
		}
		for _, rc := range injection.RCFiles() {
			changed, err := injection.RemoveFromRC(rc)
			if err != nil {
				fmt.Fprintln(os.Stderr, style.Item(style.Warn, rc+": "+err.Error()))
				continue
			}
			if changed {
				fmt.Println(style.Item(style.OK, "从 "+rc+" 删掉 PATH 行"))
			}
		}
		if fg := injection.Foreign(); len(fg) > 0 {
			// 只在我们**认得这个 agent 名**时才出声：那种情况是外来同名文件
			// 会遮住我们的 shim，值得看一眼；目录里躺着 claude-bak 这种
			// 无关文件是用户自己的事，报出来只是噪音。
			var shadow []string
			for _, n := range fg {
				if _, known := agents.Get(n); known {
					shadow = append(shadow, n)
				}
			}
			if len(shadow) > 0 {
				fmt.Println(style.Hint("同名外部文件仍在 " + injection.Dir() + "：" + strings.Join(shadow, ", ") + "（会遮住 newgate 的 shim）"))
			}
		}
		fmt.Println()
		fmt.Println(style.Hint("PATH 痕迹已清空，重开 shell 生效"))
		fmt.Println(style.Hint("这不改接管意愿：下次 newgate start 会重新装回来"))
		return 0

	default:
		return shimStatus(agents)
	}
}

// shimStatus 摊开 shim 目录的真实情况，用于排查「装了却没生效」。
func shimStatus(agents confighookapi.AgentCatalog) int {
	fmt.Println(style.Title("newgate shim", injection.Dir()))
	fmt.Println(style.Rule(72))

	if injection.InPath() {
		fmt.Println(style.Field("在 PATH", style.Green("是")))
	} else {
		fmt.Println(style.Field("在 PATH", style.Yellow("否")+style.Dim("   重开 shell 或 exec $SHELL -l")))
	}

	inst := injection.Installed()
	if len(inst) == 0 {
		fmt.Println(style.Field("已装", style.Dim("无")))
	} else {
		t := style.NewTable("shim", "指向")
		for _, n := range inst {
			a, ok := agents.Get(n)
			if !ok {
				t.Row(n, style.Dim("未知 agent"))
				continue
			}
			if real, err := a.FindReal(injection.Dir()); err == nil {
				t.Row(n, real)
			} else {
				t.Row(n, style.Red("找不到真实可执行文件（转发会失败）"))
			}
		}
		fmt.Println(style.Field("已装", fmt.Sprintf("%d 个", len(inst))))
		fmt.Print(t.String())
	}

	t := style.NewTable("rc 文件", "PATH 行")
	for _, rc := range injection.RCFiles() {
		mark := style.Dim("无")
		if injection.HasBlock(rc) {
			mark = style.Green("有")
		}
		t.Row(rc, mark)
	}
	fmt.Print(t.String())

	// 期望态 vs 现实态：只列走 shim 机制的 agent，其余与 shim 无关。
	var rows [][2]string
	for _, s := range takeover.List() {
		if s.Mechanism != takeover.MechShim {
			continue
		}
		state := style.Mark(style.OK) + " 生效"
		switch {
		case s.Wanted && !s.Active:
			state = style.Mark(style.Bad) + " 声明接管但未生效"
		case !s.Wanted && s.Active:
			state = style.Mark(style.Warn) + " 未声明接管却仍装着"
		case !s.Wanted:
			state = style.Mark(style.Skip) + " 未接管"
		}
		rows = append(rows, [2]string{s.Agent, state})
	}
	if len(rows) > 0 {
		fmt.Print(style.Section("接管意愿") + "\n")
		t := style.NewTable("agent", "状态")
		for _, r := range rows {
			t.Row(r[0], r[1])
		}
		fmt.Print(t.String())
	}

	fmt.Println()
	fmt.Println(style.Hint("底层逃生口。日常：newgate on|off <agent> · newgate shim uninstall 连 rc 一起清"))
	return 0
}

func cmdStart(agents confighookapi.AgentCatalog, force bool) int {
	if _, err := os.Stat(paths.ProvidersFile()); os.IsNotExist(err) {
		fmt.Println(style.Dim("首次运行，初始化配置"))
		if _, err := store.Init(false); err != nil {
			return die(70, err.Error())
		}
	}
	if i := daemon.Running(); i != nil {
		fmt.Println(style.Item(style.OK, fmt.Sprintf("代理已在运行   pid %d · 127.0.0.1:%d", i.PID, i.Port)))
		return 0
	}

	// 令牌先于 Spawn 落盘：daemon 一起来就要能验 /__newgate/stop。
	// 也是为了防竞态——下面 Spawn 之后 st 还会被 SaveState 写回，
	// 先确保 st 里带着令牌，写回就不会把 daemon 已生成的令牌冲掉。
	st, err := store.EnsureControlToken()
	if err != nil {
		// 不拦：用户要的是把代理起起来，为一行令牌拒绝启动更糟。但后果明说。
		fmt.Fprintln(os.Stderr, style.Mark(style.Warn)+" 控制令牌写不出去（跨用户 newgate stop 会不可用）: "+err.Error())
	}
	// 没 key 就别接管——接管了每个请求都是错误，而用户的配置已经被改了
	if probs := activeProblems(st); len(probs) > 0 && !force {
		fmt.Fprintf(os.Stderr, "newgate: 配置不可用，拒绝接管\n")
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "  %s %s\n", style.Mark(style.Bad), p)
		}
		fmt.Fprintf(os.Stderr, "%s\n", style.Hint("填 key 进 "+paths.ProvidersFile()+"，或设对应的环境变量"))
		fmt.Fprintf(os.Stderr, "%s\n", style.Hint("确认：newgate doctor    强行接管：newgate start --force"))
		return 65
	}

	info, err := daemon.Spawn(st.Port)
	if err != nil {
		return die(70, "启动守护进程失败: "+err.Error())
	}
	ok := false
	for k := 0; k < 60; k++ {
		if pingProxy(st.Port) {
			ok = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !ok {
		// 区分「进程没起来」和「起来了但我们连不上」——后者绝不能杀进程
		if httpx.TCPAlive("127.0.0.1", st.Port, time.Second) {
			fmt.Fprintf(os.Stderr,
				"newgate: 端口 %d 在听但 HTTP 探活失败，代理进程保留。\n"+
					"  最常见原因：出站代理把 loopback 请求劫走了。跑 newgate doctor。\n", st.Port)
		} else {
			fmt.Fprintf(os.Stderr, "newgate: 代理没起来（端口 %d 没在听），看日志 %s\n",
				st.Port, paths.LogFile())
			_, _ = daemon.Stop()
			return 70
		}
	}
	fmt.Println(style.Item(style.OK, fmt.Sprintf("代理已启动   pid %d · 127.0.0.1:%d · profile %s",
		info.PID, st.Port, st.DefaultProfile)))

	// 全面接管：所有没被用户显式 off 掉的 agent，各按自己的机制插上。
	fmt.Println()
	rs := takeover.OnAll(st.Port)
	printResults(rs)
	for _, r := range rs {
		if r.Mechanism == takeover.MechShim && r.Err == nil && !r.Skipped && len(r.Lines) > 0 {
			warnShellEnvConflict(agents, r.Agent)
		}
	}

	st.TakenOver = true
	// **不能吞这个错**：上面刚打印了「代理已启动」、下面还要打印「配置改动 1 秒内
	// 自动热更新」。写盘失败（CLAUDE.md §3.1 记的那个 umask 权限坑就是现场）时
	// TakenOver 没落盘，下次 start 的行为与用户以为的正相反，而屏幕上全是绿的。
	if err := store.SaveState(st); err != nil {
		return style.Die(70, "接管状态写不回 state.json："+err.Error())
	}
	fmt.Println()
	fmt.Println(style.Hint("配置改动 1 秒内自动热更新，无需重启"))
	fmt.Println(style.Hint("原配置备份 " + paths.BackupDir() + "/original/"))
	return 0
}

// printResults 把接管/释放结果打成人话。
//
// 三种标记不能混：✓ 真的接管上了，· 有意跳过，✗ 出错了。agent 名补到固定
// 宽度，续行对齐到说明列——一次接管多个 agent 时才扫得动。
func printResults(rs []takeover.Result) {
	const nameW = 10
	for _, r := range rs {
		switch {
		case r.Err != nil:
			fmt.Fprintln(os.Stderr, style.Item(style.Bad, style.Pad(r.Agent, nameW)+r.Err.Error()))
		case len(r.Lines) == 0:
			continue
		default:
			mark := style.OK
			if r.Skipped {
				mark = style.Skip
			}
			fmt.Println(style.Item(mark, style.Pad(r.Agent, nameW)+r.Lines[0]))
			for _, l := range r.Lines[1:] {
				fmt.Println("    " + style.Pad("", nameW) + style.Dim(l))
			}
		}
		for _, w := range r.Warn {
			fmt.Println(style.Bullet(style.Mark(style.Warn) + " " + w))
		}
	}
}

// cmdTakeover / cmdRelease 单独接管或释放一个 agent。
//
// 用户不该关心机制：claude 靠 PATH shim、opencode 靠改配置文件，这是我们的
// 实现细节。对外只有「接管」和「释放」，机制由 runtime/takeover 挑。
func cmdTakeover(agents confighookapi.AgentCatalog, agent string) int {
	if agent == "" {
		return die(64, "要接管谁？例：newgate on claude")
	}
	st := store.LoadState()
	r := takeover.On(agent, st.Port)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	printResults([]takeover.Result{r})
	if r.Mechanism == takeover.MechShim {
		warnShellEnvConflict(agents, agent)
	}
	if daemon.Running() == nil {
		fmt.Println(style.Hint("代理未运行 · newgate start 之后才生效"))
	}
	return 0
}

func cmdRelease(agent string) int {
	if agent == "" {
		return die(64, "要释放谁？例：newgate off claude")
	}
	r := takeover.Off(agent)
	if r.Err != nil {
		return die(65, r.Err.Error())
	}
	if len(r.Lines) == 0 {
		fmt.Println(style.Item(style.Skip, style.Pad(agent, 10)+"本来就没被接管"))
	} else {
		printResults([]takeover.Result{r})
	}
	fmt.Println(style.Hint(agent + " 已恢复直连；newgate start 不再接管它（newgate on " + agent + " 恢复）"))
	if r.Mechanism == takeover.MechShim {
		fmt.Println(style.Hint("当前 shell 可能缓存了路径：hash -r（zsh: rehash）"))
	}
	return 0
}

func cmdStop() int {
	// 顺序很重要：先把 PATH shim 摘掉。否则 `claude` 这类命令仍会命中
	// ~/.config/newgate/bin/claude → newgate，而 wrapper 会**懒启动代理**，
	// stop 等于没停——这是「stop 了还在走 newgate」的直接原因。
	//
	// OffAll 不改接管意愿：下次 start 会把这些原样插回去。
	printResults(takeover.OffAll())

	pid, err := daemon.Stop()
	if err != nil {
		return die(70, err.Error())
	}
	if pid > 0 {
		fmt.Println(style.Item(style.OK, fmt.Sprintf("代理已停止   pid %d", pid)))
	} else {
		fmt.Println(style.Item(style.Skip, "代理本来没在跑"))
	}

	st := store.LoadState()
	st.TakenOver = false
	// 同上：这一步写不出去，下次 start 会以为「用户从没放手过」。
	if err := store.SaveState(st); err != nil {
		return style.Die(70, "接管状态写不回 state.json："+err.Error())
	}

	fmt.Println()
	fmt.Println(style.Hint("所有工具已恢复直连；PATH 里那行仍指向空 shim 目录，无害"))
	fmt.Println(style.Hint("彻底清掉：newgate shim uninstall"))
	return 0
}

// cmdRestart 重启代理并保留接管现场。
//
// 优先走优雅交接（nginx upgrade 语义）：旧 daemon 把监听 socket 移交给
// 新二进制，在途请求流完为止——正穿行在代理里的会话（比如正在开发
// newgate 的 Claude Code）完全不受影响，接管状态也不动。
// 运行中的是旧版 daemon（没有交接能力）时退回 stop+start：有短暂断流
// 窗口，会提示一句。
func cmdRestart(agents confighookapi.AgentCatalog, force bool) int {
	if tryHandoff() {
		return 0
	}
	cmdStop()
	fmt.Println()
	return cmdStart(agents, force)
}

// tryHandoff 让运行中的 daemon 把监听 socket 交接给磁盘上的新二进制。
// 成功（已完全就位）返回 true；不可行时打印原因并返回 false，由调用方
// 退回 stop+start。
func tryHandoff() bool {
	i := daemon.Running()
	if i == nil {
		return false // 没在跑，restart 退化为 start
	}
	st := store.LoadState()
	if st.ControlToken == "" {
		fmt.Println(style.Item(style.Skip, "daemon 没有控制令牌（升级前启动的），退回 stop+start"))
		return false
	}
	port := i.Port
	if port <= 0 {
		port = st.Port
	}
	// 先探能力再动手：旧版 daemon 没注册 /__newgate/upgrade，盲发会被
	// catch-all 转发给上游。status 的 handoff 字段是无副作用的探针。
	supports, err := handoffSupported(port)
	if err != nil || !supports {
		if err != nil {
			fmt.Println(style.Item(style.Skip, fmt.Sprintf("探测 daemon 交接能力失败（%v），退回 stop+start", err)))
		} else {
			fmt.Println(style.Item(style.Skip, "运行中的是旧版 daemon（不支持优雅交接），退回 stop+start"))
		}
		return false
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Upgrade), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+st.ControlToken)
	// 旧 daemon 要等新进程 ready 才回 200，给足时间
	resp, err := httpx.LocalClient(20 * time.Second).Do(req)
	if err != nil {
		fmt.Println(style.Item(style.Skip, fmt.Sprintf("交接请求发不出去（%v），退回 stop+start", err)))
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Println(style.Item(style.Skip, fmt.Sprintf("交接被拒（HTTP %d: %s），退回 stop+start", resp.StatusCode, b)))
		return false
	}
	// 200 = 新进程已接上 socket、pid/lock 已改写。这里只确认它完全就位。
	for k := 0; k < 100; k++ { // 最多 5s
		if j := daemon.Running(); j != nil && j.PID != i.PID && pingProxy(j.Port) {
			fmt.Println(style.Item(style.OK, fmt.Sprintf("代理已优雅重启   pid %d → %d · 127.0.0.1:%d", i.PID, j.PID, j.Port)))
			fmt.Println(style.Hint("socket 无缝交接：在途请求由旧进程排空，会话不中断；接管状态未动"))
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("%s 交接已发出，但新进程 5 秒内没就位 · 日志 %s\n", style.Mark(style.Warn), paths.LogFile())
	return false
}

// handoffSupported 问 daemon 的 status 端点：支持优雅交接吗。
func handoffSupported(port int) (bool, error) {
	resp, err := httpx.LocalClient(3 * time.Second).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Status))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Handoff bool `json:"handoff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Handoff, nil
}

// activeProblems 只检查**实际会被用到**的 provider，别因为某个用不到的
// profile 缺 key 就挡住启动。
func activeProblems(st *domain.State) []string {
	snap, err := store.Load()
	if err != nil {
		return []string{err.Error()}
	}
	used := map[string]bool{}
	check := func(profile string) {
		for _, p := range snap.Profiles {
			if p.Name != profile {
				continue
			}
			for _, cands := range p.Roles {
				for _, b := range cands {
					used[b.Provider] = true
				}
			}
		}
	}
	check(st.DefaultProfile)
	for _, p := range st.Active {
		check(p)
	}
	var out []string
	for name := range used {
		prov, ok := snap.Providers.Providers[name]
		if !ok {
			out = append(out, fmt.Sprintf("provider %q 未定义", name))
			continue
		}
		if prov.Key() == "" {
			hint := "在 providers.json 里填 api_key"
			if prov.APIKeyEnv != "" {
				hint = "设环境变量 " + prov.APIKeyEnv + " 或填 api_key"
			}
			out = append(out, fmt.Sprintf("provider %q 没有 key → %s", name, hint))
		}
	}
	return out
}

func warnShellEnvConflict(agents confighookapi.AgentCatalog, toolID string) {
	a, ok := agents.Get(toolID)
	if !ok {
		return
	}
	var conflict []string
	for _, k := range append([]string{a.BaseURLEnv, a.AuthEnv}, a.UnsetEnv...) {
		if os.Getenv(k) != "" {
			conflict = append(conflict, k)
		}
	}
	if len(conflict) == 0 {
		return
	}
	fmt.Println(style.Hint("shell 里已导出 " + strings.Join(conflict, ", ")))
	fmt.Println(style.Hint("接管时在子进程内覆盖，不影响 newgate；off 之后重新生效（即回到直连）"))
}

// ---------- 命令声明 ----------
//
// 每一条都只做「解析 argv → 调上面的实现」。帮助行、槽位、位置是给界面看的
// 元数据，由拥有这条命令的模块声明（见 cliapi.Documented）。

// 接管那一节的位置：与原来写在界面里时一致，用户的肌肉记忆不会变。
const rankTakeover = 10

type startCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*startCommand)(nil)
	_ cliapi.Documented = (*startCommand)(nil)
)

func (startCommand) Names() []string { return []string{"start"} }

func (startCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "start", Summary: "起代理 + 接管所有 agent"}
}

func (c startCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdTakeover(c.agents, a)
	}
	return cmdStart(c.agents, cliapi.Flag(args, "--force"))
}

type stopCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*stopCommand)(nil)
	_ cliapi.Documented = (*stopCommand)(nil)
)

func (stopCommand) Names() []string { return []string{"stop"} }

func (stopCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "stop", Summary: "停代理 + 所有 agent 恢复直连"}
}

func (stopCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdRelease(a)
	}
	return cmdStop()
}

type takeoverCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*takeoverCommand)(nil)
	_ cliapi.Documented = (*takeoverCommand)(nil)
)

func (takeoverCommand) Names() []string { return []string{"on", "takeover", "take"} }

func (takeoverCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "on <agent>", Summary: "只接管一个"}
}

func (c takeoverCommand) Run(_ cliapi.Host, args []string) int {
	return cmdTakeover(c.agents, cliapi.Positional(args, 0))
}

type releaseCommand struct{}

var (
	_ cliapi.Command    = (*releaseCommand)(nil)
	_ cliapi.Documented = (*releaseCommand)(nil)
)

func (releaseCommand) Names() []string { return []string{"off", "release", "free"} }

func (releaseCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "off <agent>", Summary: "只放开一个，以后 start 也不再管它"}
}

func (releaseCommand) Run(_ cliapi.Host, args []string) int {
	if a := cliapi.Positional(args, 0); a != "" {
		return cmdRelease(a)
	}
	return cmdStop()
}

type restartCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*restartCommand)(nil)
	_ cliapi.Documented = (*restartCommand)(nil)
)

func (restartCommand) Names() []string { return []string{"restart"} }

func (restartCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionTakeover, Rank: rankTakeover,
		Usage: "restart", Summary: "重启代理，接管现场原样保留"}
}

func (c restartCommand) Run(_ cliapi.Host, args []string) int {
	return cmdRestart(c.agents, cliapi.Flag(args, "--force"))
}

type shimCommand struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.Command    = (*shimCommand)(nil)
	_ cliapi.Documented = (*shimCommand)(nil)
)

func (shimCommand) Names() []string { return []string{"shim"} }

func (shimCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionMaintenance, Rank: 50,
		Usage: "shim …", Summary: "底层逃生口，平时用 on/off 就够了"}
}

func (c shimCommand) Run(_ cliapi.Host, args []string) int {
	// 不给默认 agent：客户端 id 是**各客户端模块自己的键**，界面不该知道任何一个。
	return cmdShim(c.agents, cliapi.Positional(args, 0), cliapi.Positional(args, 1))
}

// commands 是本模块注入界面的全部命令。
func commands(agents confighookapi.AgentCatalog) []cliapi.Command {
	return []cliapi.Command{
		startCommand{agents}, stopCommand{agents}, takeoverCommand{agents},
		releaseCommand{}, restartCommand{agents}, shimCommand{agents},
	}
}
