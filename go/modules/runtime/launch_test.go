package runtime

import (
	"strings"
	"testing"

	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
)

func TestSplitLaunch(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		agent       string
		profile     string
		passthrough string
		wantErr     bool
		errHas      string
	}{
		{"bare agent", []string{"claude"}, "claude", "", "", false, ""},
		{"profile after agent, eq form", []string{"claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"profile after agent, space form", []string{"claude", "--profile", "ds"}, "claude", "ds", "", false, ""},
		{"profile before agent", []string{"--profile", "ds", "claude"}, "claude", "ds", "", false, ""},
		{"run explicit", []string{"run", "claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"passthrough flags", []string{"claude", "--resume", "abc"}, "claude", "", "--resume|abc", false, ""},
		{"passthrough after profile", []string{"claude", "--profile", "ds", "-p", "hi"}, "claude", "ds", "-p|hi", false, ""},
		{"preset alias", []string{"--preset", "toy", "claude"}, "claude", "toy", "", false, ""},
		{"unknown flag before agent", []string{"--bogus", "claude"}, "", "", "", true, "未知选项"},
		{"no agent", []string{"--profile", "ds"}, "", "", "", true, "要启动哪个 agent"},
		{"unknown agent", []string{"nope", "--profile=ds"}, "", "", "", true, "不认识的 agent"},
		{"conflicting profile", []string{"claude", "--profile=ds", "--preset=glm"}, "", "", "", true, "不一致"},
		{"profile missing value", []string{"claude", "--profile"}, "", "", "", true, "需要一个 profile 名"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agent, profile, passthrough, err := splitLaunch(agentstate.Catalog(), c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，却成功了（agent=%q profile=%q）", agent, profile)
				}
				if c.errHas != "" && !strings.Contains(err.Error(), c.errHas) {
					t.Fatalf("报错 %q 应包含 %q", err, c.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错：%v", err)
			}
			if agent != c.agent {
				t.Errorf("agent = %q，应为 %q", agent, c.agent)
			}
			if profile != c.profile {
				t.Errorf("profile = %q，应为 %q", profile, c.profile)
			}
			got := strings.Join(passthrough, "|")
			if got != c.passthrough {
				t.Errorf("passthrough = %q，应为 %q", got, c.passthrough)
			}
		})
	}
}

// TestLaunchFamilyClaimsExactlyOneHelpLine 包装启动那一族在 help 里只占一行。
//
// 每个 agent 一个 launchCommand 是**分派键**（`newgate claude` / `newgate
// opencode`），不是 help 条目——agent 名是各客户端模块的键，逐个列出来等于界面
// 又认识了一遍客户端。2026-09-18 之前 Help() 无差别返回同一行，而
// launchCommands 给每个 agent 都注册了一个实例，于是 `newgate --help` 里
// `run <agent> [args…]` 原样重复了 N 遍（本机 2 个 agent → 3 行）。
//
// 守在这里而不是界面那边：**是这一族多交了一行，不是界面漏了去重**。界面按
// 约定「空 Usage = 不占行」处理，把去重塞进界面就等于让它又认识一遍这些命令。
func TestLaunchFamilyClaimsExactlyOneHelpLine(t *testing.T) {
	catalog := testCatalog{
		"claude":   {ID: "claude", Bin: []string{"claude"}},
		"opencode": {ID: "opencode", Bin: []string{"opencode"}},
	}
	cmds := launchCommands(nil, catalog)
	if len(cmds) != 3 {
		t.Fatalf("期望 1 个 run + 2 个 agent 分派键，实际 %d 个", len(cmds))
	}

	var claimed []string
	seen := map[string]int{}
	for _, c := range cmds {
		doc, ok := c.(interface{ Help() cliapi.HelpLine })
		if !ok {
			t.Fatal("launchCommand 必须实现 Documented")
		}
		line := doc.Help()
		if line.Usage == "" {
			continue // 分派键：按约定不占行
		}
		claimed = append(claimed, line.Usage)
		seen[line.Usage]++
	}
	if len(claimed) != 1 {
		t.Fatalf("这一族只该有一行 help，实际 %d 行：%v", len(claimed), claimed)
	}
	if seen["run <agent> [args…]"] != 1 {
		t.Fatalf("`run <agent> [args…]` 应恰好出现一次，实际 %d 次", seen["run <agent> [args…]"])
	}
	// 空 HelpLine 的零值语义要成立（界面的 usageText 靠 Usage == "" 跳过）。
	if line := (launchCommand{agentID: "claude"}).Help(); line.Section != "" || line.Summary != "" {
		t.Errorf("分派键的 HelpLine 应当整个是零值，实际 %+v", line)
	}
}
