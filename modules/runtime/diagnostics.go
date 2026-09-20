package runtime

// 本文件把**接管自己的**两条体检交出去：哪些目标文件真被改写了（接管），以及
// 逃生舱准备好了没有（备份，`newgate stop` 靠它还原）。
//
// **为什么它们住在这里**（2026-09-18）：接管状态与备份目录都是本模块写出来的
// 东西；它们以前写在 modules/cli/diag.go，界面于是替接管层读 original/ 目录、
// 逐个目标文件判断有没有被改写。谁写的东西谁自己说——界面只排版
// （见 cli/extension.Diagnostic）。
//
// 依赖方向：runtime → cli/extension（叶子契约），不是 → modules/cli。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/paths"
	confighookapi "github.com/rzbdz/newgate/modules/confighook"
	"github.com/rzbdz/newgate/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/modules/runtime/daemon"
	"github.com/rzbdz/newgate/modules/runtime/takeover"
)

// 体检与 status 行的位置（Rank 小的在前）。接管排在代理之后、配置之前：它回答
// 「谁在走 newgate」。
const (
	rankStatusTakeover = 20
	rankCheckDaemon    = 35
	rankCheckTakeover  = 40
	rankCheckBackups   = 50

	// 诊断包里的位置（见 cliapi.DumpSection）。
	rankDumpTargets = 40
)

type runtimeReporter struct{ agents confighookapi.AgentCatalog }

var (
	_ cliapi.DiagnosticProvider = runtimeReporter{}
	_ cliapi.StatusProvider     = runtimeReporter{}
	_ cliapi.Dumper             = runtimeReporter{}
)

func (r runtimeReporter) Diagnostics() []cliapi.Diagnostic {
	return []cliapi.Diagnostic{checkDaemon(), r.checkTakeover(), checkBackups()}
}

// checkDaemon 「谁在服务」的三种说法必须对得上：pidfile、锁文件、真身。
//
// **为什么单独一项**（2026-09-18 现场）：pidfile 被写坏成「一个撞锁失败、当场
// 退出的子进程」之后，代理那一项会说「未运行」，而 daemon 一直在服务——两项各自
// 都没说谎，但没有一处把「你的 pidfile 和锁对不上」这句话讲出来，人只能靠 ps 猜。
// 这一项就是那句话，而且顺手把 pidfile 修回去（见 daemon.Reconcile）。
//
// 「没在跑」是 skip 不是 bad：那是正常状态，不是故障。
func checkDaemon() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckDaemon, Label: i18n.T("Daemon", nil)}
	info, notes := daemon.Reconcile()
	if len(notes) == 0 {
		// 排在前面的检查（链路、代理）都会读 Running()，它们可能已经先把
		// pidfile 修好了——那处不一致就此看不见。本进程修过就照样说出来。
		if h := daemon.LastHeal(); h != "" {
			notes = []string{h}
		}
	}
	switch {
	case len(notes) > 0:
		d.State = "warn"
		d.Line = notes[0]
		d.Details = append(d.Details, notes[1:]...)
		if info != nil {
			d.Details = append(d.Details,
				i18n.T("Trusting the lock: pid {pid} · 127.0.0.1:{port}",
					i18n.A{"pid": info.PID, "port": info.Port}),
				i18n.T("pidfile is rewritten to the real process; status/metrics recover at once, no manual file editing needed", nil))
		}
	case info == nil:
		d.State = "skip"
		d.Line = i18n.T("Not running", nil)
		d.Details = append(d.Details, "newgate start")
	default:
		d.State = "ok"
		d.Line = i18n.T("pid {pid} · 127.0.0.1:{port} · pidfile and lock agree",
			i18n.A{"pid": info.PID, "port": info.Port})
		// 优雅交接的那一瞬间 pidfile 已是新进程、锁还是旧进程（AdoptRuntime
		// 先写 pidfile 再改锁）。两个都活着却不同号时，只有这一种解释，说出来
		// 免得看到的人以为又坏了。
		if pid := daemon.LockHolder(); pid > 0 && pid != info.PID && daemon.Alive(pid) {
			d.Details = append(d.Details, i18n.T(
				"The lock file currently says pid {pid} (normal within the graceful handoff window, it lines up by itself within milliseconds)",
				i18n.A{"pid": pid}))
		}
	}
	return d
}

// Status 一行说清谁在走 newgate，有异常才展开。
//
// 期望态（on/off 过什么）和现实态（磁盘上真装了什么）不一致，正是那两个对称故障
// 的现场：接管过但没生效（工具静默直连），或者放开过但文件还指着代理。
func (r runtimeReporter) Status() []cliapi.StatusLine {
	var on, off []string
	wanted, active := 0, 0
	for _, s := range takeover.List() {
		if s.Wanted {
			wanted++
		}
		switch {
		case s.Active:
			active++
			on = append(on, s.Agent+style.Dim(" ")+style.Mark(style.OK))
		case s.Wanted:
			on = append(on, s.Agent+style.Dim(" ")+style.Mark(style.Bad))
		default:
			off = append(off, s.Agent)
		}
	}
	line := takeoverLine(on, off)
	// 代理在跑却有 agent 想接管没接管上：start/build 之后漏了一步，不补的话
	// 那个工具会静默直连。
	if _, doc := controlplane.State(); doc != nil && active < wanted {
		line += "\n" + style.Hint(style.Yellow(i18n.T("An agent declares takeover but it is not in effect; rerun newgate start", nil)))
	}
	return []cliapi.StatusLine{{Rank: rankStatusTakeover, Label: i18n.T("Takeover", nil), Value: line}}
}

func takeoverLine(on, off []string) string {
	if len(on) == 0 {
		line := style.Dim(i18n.T("All direct", nil))
		if len(off) > 0 {
			line += "   " + style.Dim("newgate start / on <agent>")
		}
		return line
	}
	line := "   " + strings.Join(on, "   ")
	if len(off) > 0 {
		line += "   " + style.Dim(strings.Join(off, " ")+" off")
	}
	return line
}

// checkTakeover 被改写的目标文件。
func (r runtimeReporter) checkTakeover() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckTakeover, Label: i18n.T("Takeover", nil)}
	var on, off []string
	for _, id := range r.agents.Names() {
		agent, ok := r.agents.Get(id)
		if !ok || agent.Config == nil {
			continue
		}
		for _, target := range agent.Config.Targets() {
			if _, err := os.Stat(target); err != nil {
				continue
			}
			if agent.Config.IsTakenOver(target) {
				on = append(on, filepath.Base(target))
			} else {
				off = append(off, filepath.Base(target))
			}
		}
	}
	if len(on) == 0 {
		d.State = "skip"
		d.Line = i18n.T("No file was rewritten", nil)
		return d
	}
	d.State = "ok"
	d.Line = strings.Join(on, " · ")
	if len(off) > 0 {
		d.Details = append(d.Details, i18n.T("Not taken over: {names}", i18n.A{"names": strings.Join(off, " · ")}))
	}
	return d
}

// checkBackups 逃生舱有没有准备好：original/ 里有东西，`newgate stop` 才还原得回去。
func checkBackups() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckBackups, Label: i18n.T("Backups", nil)}
	orig := filepath.Join(paths.BackupDir(), "original")
	ents, err := os.ReadDir(orig)
	if err != nil || len(ents) == 0 {
		d.State = "skip"
		d.Line = i18n.T("No original backups", nil)
		d.Details = append(d.Details, i18n.T("Created only after a config file has been taken over", nil))
		return d
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	d.State = "ok"
	d.Line = i18n.N("{n} original config · newgate stop can restore it",
		"{n} original configs · newgate stop can restore them", len(ents), i18n.A{"n": len(ents)})
	d.Details = append(d.Details, "backups/original/ "+strings.Join(names, " · "))
	return d
}

// ---------- 诊断包的原始素材 ----------

// Dump 交出**接管改过的那几个文件**里与 newgate 有关的片段。
//
// 只挑含 "newgate" 的行：目标是「用户原本的配置」，全文可能几百行且与问题无关，
// 而诊断包要能贴进 issue。原文（不做 JSON 解析）——坏掉的 JSON 恰恰是要看的东西。
func (r runtimeReporter) Dump() []cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpTargets, Title: i18n.T("Target files after takeover (newgate-related fragments)", nil)}
	runtimeDump := s
	var targets []string
	for _, id := range r.agents.Names() {
		agent, ok := r.agents.Get(id)
		if !ok || agent.Config == nil {
			continue
		}
		targets = append(targets, agent.Config.Targets()...)
	}
	sort.Strings(targets)
	for _, t := range targets {
		b, err := os.ReadFile(t)
		if err != nil {
			s.Lines = append(s.Lines, fmt.Sprintf("  %s : %v", t, err))
			continue
		}
		s.Lines = append(s.Lines, "  "+i18n.T("--- {file} ({n} bytes) ---", i18n.A{"file": t, "n": len(b)}))
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "newgate") {
				s.Lines = append(s.Lines, "    "+strings.TrimSpace(ln))
			}
		}
	}
	_ = runtimeDump
	return []cliapi.DumpSection{s}
}
