package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// sandboxStore 在 NEWGATE_HOME 沙箱里铺一份最小配置：一个 provider、
// 两个 profile——glm 声明了窗口，tiny 没声明。
func sandboxStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("providers.json", `{"providers":{"smt-glm":{"base_url":"https://x/v1","api_key":"k"}}}`)
	write("mappings/glm.json", `{
		"name": "glm", "priority": 20,
		"context_window": 1000000, "auto_compact_window": 500000,
		"roles": {"heavy": "smt-glm/glm-5.3", "mid": "smt-glm/glm-5.3",
			"light": "smt-glm/glm-4.5-air", "vision": "smt-glm/glm-4.5-air"}
	}`)
	// tiny 只写 heavy 的话，主力槽（四档化后是 normal）在它身上没有候选，
	// 链会掉到下一个 profile——所以这里显式给 tiny 一条 normal（也是给它
	// 的测试用例留个「主力档真名跟 profile 走」的断言点）。
	write("mappings/tiny.json", `{"name":"tiny","priority":30,
		"roles":{"heavy":"smt-glm/glm-4-plus","normal":"smt-glm/glm-4-plus"}}`)
}

// TestBuildInjectWindowEnv 窗口声明的注入。Claude Code 不认识我们注入的
// 真实模型名（glm-5.3），按「未知模型」默认 200k 窗口提前 compact——
// profile 里声明了 context_window/auto_compact_window 才有 env（2026-09
// 实测二进制 2.1.265 的解析链：MAX_CONTEXT_TOKENS 只对未知模型生效，
// AUTO_COMPACT_WINDOW 优先级最高、window=min(两者)）。
// testAgent 是内核测试用的**合成**客户端定义：真实的 claude 定义住在发行版里
// （客户端接入是产品取舍，2026-09-20 搬走）。窗口 env 的**名字**如今也来自定义
// （agentapi.Agent.ContextWindowEnv / AutoCompactEnv）——内核不认识任何一家的变量名，
// 所以这里用合成的名字，测的是「配置里声明了就按定义的名字注入」这条机制。
func testAgent() *agentapi.Agent {
	return &agentapi.Agent{
		ID:               "test-client",
		Bin:              []string{"test-client"},
		Dialect:          "anthropic",
		BaseURLEnv:       "TEST_BASE_URL",
		AuthEnv:          "TEST_API_KEY",
		ContextWindowEnv: "TEST_MAX_CONTEXT_TOKENS",
		AutoCompactEnv:   "TEST_AUTO_COMPACT_WINDOW",
		// 三个槽位与档位一一对应：这样断言读起来就是「槽位拿到自己那一档」，
		// 不需要知道任何一家客户端的槽位命名（claude 的 opus/fable/haiku 是它的知识）。
		Slots: []agentapi.Slot{
			{Name: "heavy", Tier: "heavy", EnvVar: "TEST_HEAVY_MODEL"},
			{Name: "normal", Tier: "normal", EnvVar: "TEST_NORMAL_MODEL"},
			{Name: "light", Tier: "light", EnvVar: "TEST_LIGHT_MODEL"},
		},
	}
}

func TestBuildInjectWindowEnv(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Run("声明了窗口的 profile → 两个 env 都注入", func(t *testing.T) {
		inject, _ := buildInject(a, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 1000000", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q，应为 500000", got)
		}
	})

	t.Run("没声明的 profile → 不注入", func(t *testing.T) {
		inject, _ := buildInject(a, st, "tiny", "")
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("tiny 没配 context_window，不该注入 MAX_CONTEXT_TOKENS")
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("tiny 没配 auto_compact_window，不该注入 AUTO_COMPACT_WINDOW")
		}
	})

	t.Run("不存在的 profile → fail-open 不注入", func(t *testing.T) {
		inject, _ := buildInject(a, st, "ghost", "")
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("profile 不存在时窗口 env 不该出现")
		}
	})

	t.Run("只有 context_window → 只注入一个", func(t *testing.T) {
		// 两个都独立生效：用户可以只报真实窗口、让客户端自己定 compact 点。
		dir := os.Getenv("NEWGATE_HOME")
		if err := os.WriteFile(filepath.Join(dir, "mappings", "glm.json"),
			[]byte(`{"name":"glm","priority":20,"context_window":1000000,
				"roles":{"heavy":"smt-glm/glm-5.3"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		inject, _ := buildInject(a, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 1000000", got)
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("没配 auto_compact_window 就不该注入")
		}
	})
}

// TestBuildInjectModelNames 槽位命名的两种模式（2026-09-09 定稿）：
// 默认动态——槽位保持档位名，--set-profile 对跑着的会话立刻生效；
// 显式 --profile 钉死——注入真实模型名，显示的就是这次真用的。
func TestBuildInjectModelNames(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Run("默认（动态）：槽位 = 档位名，base URL 不带 /p/", func(t *testing.T) {
		inject, _ := buildInject(a, st, "glm", "")
		if got := inject["TEST_BASE_URL"]; got != "http://127.0.0.1:8899/a/test-client" {
			t.Errorf("BASE_URL = %q", got)
		}
		// 四档化（2026-09-16）：opus 槽 = 主力档 normal，fable 槽才是 heavy
		if got := inject["TEST_NORMAL_MODEL"]; got != "normal" {
			t.Errorf("动态模式槽位应是档位名，NORMAL_MODEL = %q", got)
		}
		if got := inject["TEST_HEAVY_MODEL"]; got != "heavy" {
			t.Errorf("HEAVY_MODEL = %q，应为 heavy", got)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "light" {
			t.Errorf("LIGHT_MODEL = %q，应为 light", got)
		}
	})

	t.Run("显式 profile（钉死）：槽位 = 真实模型名，base URL 带 /p/", func(t *testing.T) {
		inject, _ := buildInject(a, st, "glm", "glm")
		if got := inject["TEST_BASE_URL"]; got != "http://127.0.0.1:8899/a/test-client/p/glm" {
			t.Errorf("BASE_URL = %q", got)
		}
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-5.3" {
			t.Errorf("钉死模式应是真实名，NORMAL_MODEL = %q", got)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "glm-4.5-air" {
			t.Errorf("LIGHT_MODEL = %q，应为 glm-4.5-air", got)
		}
	})

	t.Run("钉死到别的 profile：真名跟被选中的 profile 走", func(t *testing.T) {
		// 默认链头是 glm，--profile tiny：真名必须是 tiny 的，不然界面
		// 显示的和实际跑的对不上
		inject, _ := buildInject(a, st, "tiny", "tiny")
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-4-plus" {
			t.Errorf("NORMAL_MODEL = %q，应为 tiny 的 glm-4-plus", got)
		}
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("窗口应该跟被选中的 profile（tiny，没声明）走")
		}
	})
}

// TestBuildInjectOverridesInherited execReal 的 env 白名单保证注入值赢过
// 用户 shell 里残留的同名变量——「切了没生效」最常见的成因。
// 这里只验证 buildInject 产出正确；覆盖发生在 execReal，由 e2e 盖着。
func TestBuildInjectOverridesInherited(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Setenv("TEST_AUTO_COMPACT_WINDOW", "666") // 用户自己设过别的值
	inject, _ := buildInject(a, st, "glm", "")
	if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
		t.Fatalf("profile 的声明应该赢过用户环境里的旧值: %q", got)
	}
}

// TestBuildInjectWarnsWhenConfigUnreadable 配置读不出来时必须留一句话。
//
// 这条路径是 fail-open 的：读不到配置就退回「槽位保持档位名」，代理仍能按档位
// 名路由，所以这次调用照样起得来。但它同时**少注入**了两样东西——真实模型名，
// 以及 profile 声明的窗口（不给的话 Claude Code 按 200k 假设提前 compact）。
// 后者是用户能直接感觉到的行为变化，而 2026-09-18 之前这条 if 没有 else，
// 整块注入静默跳过，一个字都不说。
func TestBuildInjectWarnsWhenConfigUnreadable(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	// 把 providers.json 弄坏：Load 在第一步就失败。
	p := filepath.Join(os.Getenv("NEWGATE_HOME"), "providers.json")
	if err := os.WriteFile(p, []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}

	inject, warns := buildInject(a, st, "glm", "glm")
	if len(warns) == 0 {
		t.Fatal("配置读不出来时必须给出警告，否则用户只看到「窗口没生效」而不知道原因")
	}
	// fail-open：槽位仍然注入了（档位名），只是不是真实模型名。
	if got := inject["TEST_NORMAL_MODEL"]; got != "normal" {
		t.Errorf("读不到配置时槽位应保持档位名 normal，实际 %q", got)
	}
	if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
		t.Error("读不到配置时不该有窗口声明")
	}
}
