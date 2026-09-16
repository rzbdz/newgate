package cli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/gateway/dialect"
	"github.com/rzbdz/newgate/go/modules/gateway/health"
	"github.com/rzbdz/newgate/go/modules/gateway/metrics"
	"github.com/rzbdz/newgate/go/modules/gateway/probe"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
	"github.com/rzbdz/newgate/go/modules/runtime/takeover"
)

func warnShellEnvConflict(agents agentapi.AgentCatalog, toolID string) {
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

// cmdProbe 主动探活：对每个候选真打一发最小请求。
//
// 版式：进度走 stderr（stdout 留给结果表，`probe > f` 拿到的是干净的表），
// 结果一段三块——明细表 / 方言能力 / 汇总。以前进度和结果都往 stdout 里
// 混着写，重定向之后是一堆进度行夹着表格。
func cmdProbe(only string, asJSON bool) int {
	quiet := asJSON // JSON 模式不打进度，免得污染输出
	started := time.Now()
	st := store.LoadState()

	opts := probe.Options{
		Only:        only,
		Timeout:     st.Timeouts.ClassifierFirstByte(),
		SlowAfter:   st.Timeouts.ClassifierFirstByte(),
		Concurrency: 8,
		WaitTick:    5 * time.Second,
	}
	if !quiet {
		opts.OnPlan = func(ts []probe.Target) {
			fmt.Fprintf(os.Stderr, "探测 %d 个目标（并发 %d，同 (provider, model) 只打一次）\n",
				len(ts), opts.Concurrency)
			for _, t := range ts {
				fmt.Fprintf(os.Stderr, "  %s %s\n", style.Mark(style.Skip), t)
			}
			fmt.Fprintln(os.Stderr)
		}
		opts.OnDone = func(t probe.Target, status int, lat, totalLat time.Duration, err error) {
			mark := style.Mark(probeMark(err == nil && status == 200))
			st := "  -"
			if status > 0 {
				st = fmt.Sprintf("%3d", status)
			}
			msg := ""
			if err != nil {
				msg = "   " + firstLine(err.Error())
			}
			line := fmt.Sprintf("%s %s %s 主%dms 总%dms%s",
				mark, t.String(),
				st, lat.Milliseconds(), totalLat.Milliseconds(), msg)
			fmt.Fprintln(os.Stderr, style.WrapLine(line, "    "))
		}
		opts.OnWaiting = func(inflight map[probe.Target]time.Duration) {
			var parts []string
			for t, d := range inflight {
				parts = append(parts, fmt.Sprintf("%s %ds", t, int(d.Seconds())))
			}
			sort.Strings(parts)
			for _, part := range parts {
				fmt.Fprintln(os.Stderr, style.WrapLine(
					"        "+style.Mark(style.Skip)+" 等待中: "+part, "          "))
			}
		}
	}

	results, err := probe.Run(opts)
	if err != nil {
		return die(65, err.Error())
	}
	opened, healthErr := publishProbeHealth(results)
	if healthErr != nil && !quiet {
		fmt.Fprintln(os.Stderr, style.Item(style.Warn, "全局熔断表未更新："+healthErr.Error()))
	}
	if asJSON {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(b))
		if probe.FailedCount(results) > 0 {
			return 1
		}
		return 0
	}

	head := "全部 profile"
	if only != "" {
		head = "profile " + only
	}
	fmt.Println(style.Title("newgate probe", fmt.Sprintf("%s · %d 目标 · %s",
		head, probe.UniqueCount(results), time.Since(started).Round(time.Millisecond))))
	fmt.Println(style.Rule(72))

	// 明细：同一 profile 的后续行不再重复 profile 名（视觉分组，省一列宽度）。
	t := style.NewTable("profile", "档位", "上游/模型", "评级", "主延迟", "总耗时")
	last := ""
	var probeErrors []string
	for _, r := range results {
		p := r.Profile
		if p == last {
			p = ""
		} else {
			last = r.Profile
		}
		lat := style.Dim("-")
		if r.Latency > 0 {
			lat = fmt.Sprintf("%dms", r.Latency.Milliseconds())
		}
		if r.Err != "" {
			probeErrors = append(probeErrors, fmt.Sprintf("%s/%s：HTTP %d · %s",
				r.Provider, r.Model, r.Status, firstLine(r.Err)))
		}
		totalLat := style.Dim("-")
		if r.Total > 0 {
			totalLat = fmt.Sprintf("%dms", r.Total.Milliseconds())
		}
		t.Row(p, r.Role, r.Provider+"/"+r.Model,
			probeGrade(r, opts.SlowAfter), lat, totalLat)
	}
	fmt.Print(t.String())
	for _, detail := range probeErrors {
		fmt.Println(style.Item(style.Bad, detail))
	}
	fmt.Println(style.Hint("健康评分只用主探活延迟；总耗时还包含方言和 quirk 检查"))

	// 方言能力：probe 顺带探明的。count_tokens ✗ 的上游，Claude Code 的
	// 水位条走本地粗估（forward 层 lazy probe 也会自己学到这一点）。
	if entries := dialect.Snapshot(); len(entries) > 0 {
		fmt.Print(style.Section("方言能力") + style.Dim("   ✓ 支持  ✗ 探过不支持  ? 未探") + "\n")
		d := style.NewTable("上游/模型", "openai", "anthropic", "count_tokens")
		for _, e := range entries {
			d.Row(e.Provider+"/"+e.Model,
				dialectMark(e, dialect.CapOpenAI),
				dialectMark(e, dialect.CapAnthropic),
				dialectMark(e, dialect.CapCountTokens))
		}
		fmt.Print(d.String())
	}

	sums := probe.Summarize(results)
	fmt.Print(style.Section("汇总") + "\n")
	s := style.NewTable("profile", "通", "挂", "平均延迟", "结论")
	s.AlignRight(1)
	s.AlignRight(2)
	s.AlignRight(3)
	var healthy []string
	for _, sm := range sums {
		concl := style.Green("可用")
		switch {
		case sm.OK == 0:
			concl = style.Red("不可用")
		case sm.Bad > 0:
			concl = style.Yellow("部分可用")
		case sm.Grade == "fluent":
			concl = style.Green("流畅")
		}
		avg := style.Dim("-")
		if sm.OK > 0 {
			avg = fmt.Sprintf("%dms", sm.AvgMs)
			if sm.Bad == 0 {
				healthy = append(healthy, fmt.Sprintf("%s(%dms)", sm.Profile, sm.AvgMs))
			}
		}
		s.Row(sm.Profile, fmt.Sprintf("%d", sm.OK), fmt.Sprintf("%d", sm.Bad), avg, concl)
	}
	fmt.Print(s.String())

	fmt.Println(style.Hint("当前 profile：" + st.DefaultProfile))
	if healthErr == nil {
		fmt.Println(style.Hint(fmt.Sprintf(
			"全局熔断表已更新：本轮目标中仍有 %d 个 binding 熔断（慢阈值 %s）",
			opened, st.Timeouts.ClassifierFirstByte())))
		if opened > 0 {
			fmt.Println(style.Hint("至少隔离 60s；之后仅成功 probe 可以恢复"))
		}
	}

	// 当前 profile 有挂的就给出建议
	for _, sm := range sums {
		if sm.Profile == st.DefaultProfile && sm.Bad > 0 {
			fmt.Println()
			fmt.Println(style.Item(style.Warn, fmt.Sprintf("当前 profile %s 有 %d 个档位不可用", sm.Profile, sm.Bad)))
			if len(healthy) > 0 {
				fmt.Println(style.Bullet("全绿的：" + strings.Join(healthy, " · ")))
				fmt.Println(style.Bullet(style.Cyan("newgate --set-profile " + strings.SplitN(healthy[0], "(", 2)[0])))
			} else {
				fmt.Println(style.Bullet("没有全绿的 profile"))
			}
		}
	}
	if probe.FailedCount(results) > 0 {
		return 1
	}
	return 0
}