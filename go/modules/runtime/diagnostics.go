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

	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	confighookapi "github.com/rzbdz/newgate/go/modules/confighook"
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/go/modules/runtime/takeover"
)

// 体检与 status 行的位置（Rank 小的在前）。接管排在代理之后、配置之前：它回答
// 「谁在走 newgate」。
const (
	rankStatusTakeover = 20
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
	return []cliapi.Diagnostic{r.checkTakeover(), checkBackups()}
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
		line += "\n" + style.Hint(style.Yellow("有 agent 声明接管但未生效，重跑 newgate start"))
	}
	return []cliapi.StatusLine{{Rank: rankStatusTakeover, Label: "接管", Value: line}}
}

func takeoverLine(on, off []string) string {
	if len(on) == 0 {
		line := style.Dim("全部直连")
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
	d := cliapi.Diagnostic{Rank: rankCheckTakeover, Label: "接管"}
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
		d.Line = "无文件被改写"
		return d
	}
	d.State = "ok"
	d.Line = strings.Join(on, " · ")
	if len(off) > 0 {
		d.Details = append(d.Details, "未接管："+strings.Join(off, " · "))
	}
	return d
}

// checkBackups 逃生舱有没有准备好：original/ 里有东西，`newgate stop` 才还原得回去。
func checkBackups() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckBackups, Label: "备份"}
	orig := filepath.Join(paths.BackupDir(), "original")
	ents, err := os.ReadDir(orig)
	if err != nil || len(ents) == 0 {
		d.State = "skip"
		d.Line = "无原始备份"
		d.Details = append(d.Details, "接管过配置文件之后才会生成")
		return d
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	d.State = "ok"
	d.Line = fmt.Sprintf("%d 份原配置 · newgate stop 可还原", len(ents))
	d.Details = append(d.Details, "backups/original/ "+strings.Join(names, " · "))
	return d
}

// ---------- 诊断包的原始素材 ----------

// Dump 交出**接管改过的那几个文件**里与 newgate 有关的片段。
//
// 只挑含 "newgate" 的行：目标是「用户原本的配置」，全文可能几百行且与问题无关，
// 而诊断包要能贴进 issue。原文（不做 JSON 解析）——坏掉的 JSON 恰恰是要看的东西。
func (r runtimeReporter) Dump() []cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpTargets, Title: "接管后的目标文件（newgate 相关片段）"}
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
		s.Lines = append(s.Lines, fmt.Sprintf("  --- %s (%d 字节) ---", t, len(b)))
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "newgate") {
				s.Lines = append(s.Lines, "    "+strings.TrimSpace(ln))
			}
		}
	}
	_ = runtimeDump
	return []cliapi.DumpSection{s}
}
