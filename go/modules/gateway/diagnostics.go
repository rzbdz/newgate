package gateway

// 本文件把**数据面自己的**两条体检交出去：出站代理会不会劫走 loopback（环境），
// 以及进程与端口那一对失败要分开说（代理）。
//
// **为什么它们住在这里**（2026-09-18）：这两条问的都是「请求到得了网关吗」，答案
// 的一半在网关自己的控制面客户端里（controlplane），另一半在网关自己的转发语义
// 里；而它们以前写在 modules/cli/diag.go，界面于是替数据面做着 HTTP 探活与端口
// 判断。谁的事谁自己说——界面只负责排版（见 cli/extension.Diagnostic）。
//
// 依赖方向：gateway → cli/extension（叶子契约），不是 → modules/cli。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate/go/lib/durarg"
	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/controlplane"
)

// 体检与 status 行的位置（Rank 小的在前）。代理排在接管与配置之前：它挂了所有
// 走 newgate 的工具一起挂。
const (
	rankStatusProxy = 10
	rankCheckEnv    = 20
	rankCheckProxy  = 30

	// 诊断包里的位置（见 cliapi.DumpSection）。证据与日志排在配置之后：
	// 前面是「配置长什么样」，后面是「真发出去过什么」。
	rankDumpEvidence = 50
	rankDumpLog      = 60
)

type gatewayReporter struct{}

var (
	_ cliapi.DiagnosticProvider = gatewayReporter{}
	_ cliapi.StatusProvider     = gatewayReporter{}
	_ cliapi.Dumper             = gatewayReporter{}
)

func (gatewayReporter) Diagnostics() []cliapi.Diagnostic {
	return []cliapi.Diagnostic{checkEnv(), checkProxy()}
}

// Status 一行说清代理在不在跑。
//
// 三种状态分开表达：进程死了可以重启；**端口在听却探不通**是环境问题，重启一百
// 次也没用（常见于出站代理劫持 loopback）。这一行只给结论，展开在 doctor 里。
func (gatewayReporter) Status() []cliapi.StatusLine {
	info, doc := controlplane.State()
	var value string
	switch {
	case info == nil:
		value = style.Dim("未运行") + "    newgate start"
	case doc == nil:
		value = style.Yellow("端口无响应") + fmt.Sprintf("   pid %d · 127.0.0.1:%d", info.PID, info.Port)
	default:
		value = style.Green("● 运行中") + fmt.Sprintf("   pid %d · 127.0.0.1:%d · %s · %dreq/%derr",
			info.PID, info.Port, durarg.Format(doc.UptimeS), doc.Requests, doc.Failures)
	}
	return []cliapi.StatusLine{{Rank: rankStatusProxy, Label: "代理", Value: value}}
}

// checkEnv 出站代理会不会把 loopback 请求劫走。
//
// 这是最隐蔽的一类故障：newgate 日志里一条请求都没有，因为请求根本没到我们
// 这——客户端发给了 http_proxy，代理连不上 127.0.0.1 就回 502。
func checkEnv() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckEnv, Label: "环境"}
	set := proxyEnvSet()
	if len(set) == 0 {
		d.State = "ok"
		d.Line = "无出站代理变量"
		return d
	}
	noProxy := os.Getenv("no_proxy") + "," + os.Getenv("NO_PROXY")
	covered := 0
	for _, want := range []string{"127.0.0.1", "localhost"} {
		if strings.Contains(noProxy, want) {
			covered++
		}
	}
	if covered == 2 {
		d.State = "ok"
		d.Line = "有出站代理；NO_PROXY 已放行 loopback"
		d.Details = append(d.Details, set...)
		return d
	}
	d.State = "bad"
	d.Line = "有出站代理；NO_PROXY 未放行 127.0.0.1 / localhost"
	d.Details = append(d.Details, set...)
	d.Details = append(d.Details,
		"后果：客户端把 127.0.0.1:8899 的请求交给出站代理，连不上即 502；",
		"      newgate 日志不会留下任何记录。",
		"修复（写进 shell rc 后重开客户端）：",
		`  export NO_PROXY="127.0.0.1,localhost,::1,$NO_PROXY"`,
		`  export no_proxy="$NO_PROXY"`)
	return d
}

func proxyEnvSet() []string {
	var out []string
	for _, n := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"all_proxy", "ALL_PROXY"} {
		if v := os.Getenv(n); v != "" {
			out = append(out, n+"="+v)
		}
	}
	return out
}

// checkProxy 代理进程与端口的两种失败要分开说：进程死了可以重启，
// 端口在听却探不通是环境问题，重启一百次也没用。
func checkProxy() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckProxy, Label: "代理"}
	st := store.LoadState()
	info, doc := controlplane.State()
	switch {
	case info == nil:
		d.State = "skip"
		d.Line = fmt.Sprintf("未运行（端口 %d）", st.Port)
		d.Details = append(d.Details, "newgate start")
	case doc != nil:
		d.State = "ok"
		d.Line = fmt.Sprintf("pid %d · 127.0.0.1:%d · %s", info.PID, info.Port, durarg.Format(doc.UptimeS))
	default:
		d.State = "bad"
		d.Line = fmt.Sprintf("pid %d 存活，127.0.0.1:%d HTTP 探活失败", info.PID, info.Port)
		d.Details = append(d.Details,
			"进程正常，请求到不了它；常见于出站代理劫持 loopback（见「环境」一项）。")
	}
	return d
}

// ---------- 诊断包的原始素材 ----------

func (gatewayReporter) Dump() []cliapi.DumpSection {
	return []cliapi.DumpSection{dumpEvidence(), dumpLogTail()}
}

// dumpEvidence 列出上游报错时无条件落盘的证据文件。
//
// 它是排查「是不是代理改坏了请求」唯一能拿出手的东西（docs/11），所以连用法
// 一起写进诊断包——拿到包的人不必再去翻文档。
func dumpEvidence() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpEvidence, Title: "错误证据文件"}
	dir := filepath.Join(paths.Config(), "dump")
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		s.Lines = append(s.Lines, "  （无。上游报 4xx/5xx 时会自动生成）")
		return s
	}
	for _, e := range ents {
		info, ierr := e.Info()
		size := int64(0)
		if ierr == nil {
			size = info.Size()
		}
		s.Lines = append(s.Lines, fmt.Sprintf("  %s  %d 字节", filepath.Join(dir, e.Name()), size))
	}
	s.Lines = append(s.Lines,
		"",
		"  看「我们发出的」和「客户端发来的」差在哪：",
		fmt.Sprintf("    diff <(jq -S . %s/err-*.client-sent.json) \\", dir),
		fmt.Sprintf("         <(jq -S . %s/err-*.we-sent.json)", dir))
	return s
}

// dumpLogTail 日志全文。诊断包就是要原文，所以不做截断——它由用户自己决定怎么用。
func dumpLogTail() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpLog, Title: "日志全文"}
	b, err := os.ReadFile(paths.LogFile())
	if err != nil {
		s.Lines = append(s.Lines, "  读不到: "+err.Error())
		return s
	}
	s.Lines = append(s.Lines, strings.Split(strings.TrimRight(string(b), "\n"), "\n")...)
	return s
}
