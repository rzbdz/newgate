package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/core/domain"
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
	write("mappings/tiny.json", `{"name":"tiny","priority":30,"roles":{"heavy":"smt-glm/glm-4-plus"}}`)
}

// TestBuildInjectWindowEnv 窗口声明的注入。Claude Code 不认识我们注入的
// 真实模型名（glm-5.3），按「未知模型」默认 200k 窗口提前 compact——
// profile 里声明了 context_window/auto_compact_window 才有 env（2026-09
// 实测二进制 2.1.265 的解析链：MAX_CONTEXT_TOKENS 只对未知模型生效，
// AUTO_COMPACT_WINDOW 优先级最高、window=min(两者)）。
func TestBuildInjectWindowEnv(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := agents.Registry["claude"]

	t.Run("声明了窗口的 profile → 两个 env 都注入", func(t *testing.T) {
		inject := buildInject(a, st, "glm", "")
		if got := inject["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 1000000", got)
		}
		if got := inject["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; got != "500000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q，应为 500000", got)
		}
	})

	t.Run("没声明的 profile → 不注入", func(t *testing.T) {
		inject := buildInject(a, st, "tiny", "")
		if _, has := inject["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; has {
			t.Error("tiny 没配 context_window，不该注入 MAX_CONTEXT_TOKENS")
		}
		if _, has := inject["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; has {
			t.Error("tiny 没配 auto_compact_window，不该注入 AUTO_COMPACT_WINDOW")
		}
	})

	t.Run("不存在的 profile → fail-open 不注入", func(t *testing.T) {
		inject := buildInject(a, st, "ghost", "")
		if _, has := inject["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; has {
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
		inject := buildInject(a, st, "glm", "")
		if got := inject["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 1000000", got)
		}
		if _, has := inject["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; has {
			t.Error("没配 auto_compact_window 就不该注入")
		}
	})
}

// TestBuildInjectModelNames 真实模型名注入 + base URL 的 profile 覆盖，
// 顺带确认窗口注入没有破坏这两条老行为。
func TestBuildInjectModelNames(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := agents.Registry["claude"]

	t.Run("默认：base URL 不带 /p/，模型名是真实名", func(t *testing.T) {
		inject := buildInject(a, st, "glm", "")
		if got := inject["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:8899/a/claude" {
			t.Errorf("BASE_URL = %q", got)
		}
		if got := inject["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "glm-5.3" {
			t.Errorf("OPUS_MODEL = %q，应为 glm-5.3", got)
		}
		if got := inject["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "glm-4.5-air" {
			t.Errorf("HAIKU_MODEL = %q，应为 glm-4.5-air", got)
		}
	})

	t.Run("显式 profile：base URL 带 /p/，窗口跟 profile 走", func(t *testing.T) {
		// 默认链头是 glm（有窗口），--profile tiny（没窗口）：窗口 env
		// 必须跟 tiny 走，而不是跟默认链头——否则用户切到小窗口 profile
		// 时拿到的还是大窗口声明。
		inject := buildInject(a, st, "tiny", "tiny")
		if got := inject["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:8899/a/claude/p/tiny" {
			t.Errorf("BASE_URL = %q", got)
		}
		if _, has := inject["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; has {
			t.Error("窗口应该跟被选中的 profile（tiny）走")
		}
	})
}

// TestBuildInjectOverridesInherited execReal 的 env 白名单保证注入值赢过
// 用户 shell 里残留的同名变量——「切了没生效」最常见的成因。
// 这里只验证 buildInject 产出正确；覆盖发生在 execReal，由 e2e 盖着。
func TestBuildInjectOverridesInherited(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := agents.Registry["claude"]

	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "666") // 用户自己设过别的值
	inject := buildInject(a, st, "glm", "")
	if got := inject["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; got != "500000" {
		t.Fatalf("profile 的声明应该赢过用户环境里的旧值: %q", got)
	}
}
