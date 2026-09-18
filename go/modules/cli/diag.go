package cli

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/lib/buildinfo"
	"github.com/rzbdz/newgate/go/lib/durarg"
	"github.com/rzbdz/newgate/go/lib/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/store"

	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
)

// prettyMs 毫秒 → 人话。链预算是按 ms 配的（state.json 里 120000），
// 打印时不该原样甩 120000ms 给用户。
func prettyMs(ms int) string {
	return (time.Duration(ms) * time.Millisecond).String()
}

func prettyDur(sec int) string { return durarg.Format(sec) }

// cmdDoctor 体检。
//
// **界面在这里一项都不认识**：每一行都由拥有那件事的模块经 RegisterDiagnostics
// 报上来（文件/链路归 config，环境/代理归 gateway，接管/备份归 runtime），界面只
// 做统一的排版与「有没有红的」这个结论。上一版这里写死了六项，于是界面为了它们
// import 了 paths / store / resolve / takeover ——那都是别人的知识。
func cmdDoctor(service *service) int {
	fmt.Println(style.Title("newgate doctor", buildinfo.Version()))
	fmt.Println(style.Rule(64))

	checks := service.moduleDiagnostics()
	fmt.Println()
	bad := 0
	for _, d := range checks {
		mark := style.Skip
		switch d.State {
		case "ok":
			mark = style.OK
		case "warn":
			mark = style.Warn
		case "bad":
			mark = style.Bad
		}
		if mark == style.Bad {
			bad++
		}
		fmt.Println(style.Field(d.Label, style.Mark(mark)+" "+d.Line))
		for _, detail := range d.Details {
			fmt.Println(style.Bullet(style.Dim(detail)))
		}
	}

	fmt.Println()
	if bad == 0 {
		fmt.Println(style.Green("全部通过"))
		return 0
	}
	fmt.Printf("%s %d 项异常\n", style.Mark(style.Bad), bad)
	return 1
}

// sortedKeys 稳定顺序的 map 键（诊断包里几处按名字列 provider 用）。
func sortedKeys(m map[string]domain.Provider) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cmdAllLogs(agents agentapi.AgentCatalog, service *service) int {
	line := func(t string) { fmt.Printf("\n===== %s =====\n", t) }

	line("版本与环境")
	fmt.Println(VersionLine())
	fmt.Printf("配置目录 %s\n", paths.Config())
	fmt.Printf("日志     %s\n", paths.LogFile())
	for _, k := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"no_proxy", "NO_PROXY", "NEWGATE_HOME", "NEWGATE_TARGET_DIR", "NEWGATE_DUMP"} {
		if v := os.Getenv(k); v != "" {
			fmt.Printf("env      %s=%s\n", k, v)
		}
	}

	line("状态")
	cmdStatus(service)

	line("providers.json（密钥脱敏）")
	if provs, err := store.LoadProviders(); err == nil {
		for _, n := range sortedKeys(provs.Providers) {
			p := provs.Providers[n]
			k := "(空)"
			if v := p.Key(); v != "" {
				if len(v) > 10 {
					k = v[:7] + "…" + fmt.Sprint(len(v)) + "字符"
				} else {
					k = "(过短)"
				}
			}
			fmt.Printf("  %-14s %-45s protocol=%-10s key=%s\n", n, p.BaseURL, p.Protocol, k)
			// 两种方言分家的上游：另一个 base 也报出来，否则「claude 的流量
			// 到底发去哪」在 doctor 里是黑盒。
			if p.AnthropicURL != "" {
				fmt.Printf("  %-14s %-45s （anthropic 方言走这条）\n", "", p.AnthropicURL)
			}
		}
	}

	line("所有 profile 的绑定")
	names, _ := store.ListProfiles()
	st := store.LoadState()
	for _, n := range names {
		mark := " "
		if n == st.DefaultProfile {
			mark = "*"
		}
		fb := ""
		if n == st.DefaultProfile {
			fb = "  (备用)"
		}
		fmt.Printf(" %s %s%s\n", mark, n, fb)
		if pr, err := store.LoadProfile(n); err == nil {
			for _, role := range domain.Roles {
				if b, ok := pr.Resolve(role); ok {
					fmt.Printf("      %-8s %s/%s\n", role, b.Provider, b.Model)
				}
			}
		}
	}

	line("接管后的目标文件（newgate 相关片段）")
	var configTargets []string
	for _, id := range agents.Names() {
		agent, ok := agents.Get(id)
		if !ok || agent.Config == nil {
			continue
		}
		configTargets = append(configTargets, agent.Config.Targets()...)
	}
	sort.Strings(configTargets)
	for _, t := range configTargets {
		b, err := ioutil.ReadFile(t)
		if err != nil {
			fmt.Printf("  %s : %v\n", t, err)
			continue
		}
		fmt.Printf("  --- %s (%d 字节) ---\n", t, len(b))
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "newgate") {
				fmt.Printf("    %s\n", strings.TrimSpace(ln))
			}
		}
	}

	line("错误证据文件")
	dumpDir := filepath.Join(paths.Config(), "dump")
	ents, err := ioutil.ReadDir(dumpDir)
	if err != nil || len(ents) == 0 {
		fmt.Println("  （无。上游报 4xx/5xx 时会自动生成）")
	} else {
		for _, e := range ents {
			fmt.Printf("  %s  %d 字节\n", filepath.Join(dumpDir, e.Name()), e.Size())
		}
		fmt.Println("\n  看「我们发出的」和「客户端发来的」差在哪：")
		fmt.Printf("    diff <(jq -S . %s/err-*.client-sent.json) \\\n", dumpDir)
		fmt.Printf("         <(jq -S . %s/err-*.we-sent.json)\n", dumpDir)
	}

	line("日志全文")
	b, err := ioutil.ReadFile(paths.LogFile())
	if err != nil {
		fmt.Printf("  读不到: %v\n", err)
	} else {
		fmt.Print(string(b))
	}
	return 0
}

// cmdStatus 一屏概览。
//
// **这一屏的每一行都归它的所有者**：谁的状态谁自己报（RegisterStatus /
// RegisterStatusBlocks）。界面在这里一行都不认识——代理、接管、配置各由
// gateway / runtime / config 报上来，界面的全部工作是按 Rank 排一下然后打印。
func cmdStatus(service *service) int {
	fmt.Println(style.Title("newgate "+buildinfo.Version(), buildinfo.BuildTimeDisplay()))
	fmt.Println(style.Rule(64))

	for _, line := range service.statusLines() {
		fmt.Println(style.Field(line.Label, line.Value))
	}
	for _, block := range service.statusBlocks() {
		if block.Title != "" {
			fmt.Print(style.Section(block.Title) + "\n")
		}
		for _, l := range block.Lines {
			fmt.Println(l)
		}
	}
	return 0
}

// routingOf 已被 runtime/takeover.List() 取代——那里是接管状态的唯一事实源，
// CLI / TUI / Web 都读同一份，不再各自判断一遍机制。
