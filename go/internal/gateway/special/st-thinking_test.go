package special

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/internal/gateway/quirk"
)

// markAlwaysThinks 模拟「转发时撞过一次 1210」之后的 quirk 注册表状态。
// 注册表是全局的，用完必须 Reset，别脏了别的测试。
func markAlwaysThinks(t *testing.T, provider, model string) {
	t.Helper()
	quirk.Reset()
	quirk.Mark(provider, model, quirk.NoThinkingDisable)
	t.Cleanup(quirk.Reset)
}

func TestAlwaysThinksMatch(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	cases := []struct {
		name string
		r    *Request
		want bool
	}{
		{"quirk 学过的模型", req("glm-5.3", "smt-glm", "https://x/v1"), true},
		{"同 provider 别的模型", req("glm-4.5-air", "smt-glm", "https://x/v1"), false},
		{"同模型别的 provider", req("glm-5.3", "other", "https://x/v1"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := (alwaysThinks{}).Match(c.r); got != c.want {
			t.Errorf("%s: Match=%v want %v", c.name, got, c.want)
		}
	}
}

func TestAlwaysThinksApply(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	r := req("glm-5.3", "smt-glm", "https://x/v1")

	t.Run("disabled → enabled + 补 effort", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"disabled"},"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("改完不是合法 JSON: %v", err)
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
			t.Fatalf("thinking 应为 enabled，实际 %v", m["thinking"])
		}
		if m["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort 应补 low，实际 %v", m["reasoning_effort"])
		}
		if len(notes) != 2 {
			t.Fatalf("两笔改动都要有 note，实际 %v", notes)
		}
	})

	t.Run("adaptive 不动（客户端显式要思考）", func(t *testing.T) {
		// 回归（2026-09-09 实抓）：quirk 学到之后，主循环每个流式请求都被
		// 塞了 reasoning_effort:low，压低了 adaptive 思考的深度。显式带了
		// thinking 的请求本来就 200，一个字节都不该动。
		body := []byte(`{"model":"glm-5.3","stream":true,"max_tokens":32000,` +
			`"thinking":{"type":"adaptive"},"tools":[{"name":"Bash"}],"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("adaptive 请求不该被动: out=%s notes=%v", out, notes)
		}
	})

	t.Run("没 thinking 但带 tools → 只补 effort", func(t *testing.T) {
		// 原始 1210 场景：tools 在场而没写 thinking，聚合器隐式替我们关
		// 思考，glm-5.3 直接拒。
		body := []byte(`{"model":"glm-5.3","tools":[{"name":"Bash"}],"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if m["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort 应补 low，实际 %v", m["reasoning_effort"])
		}
		if _, has := m["thinking"]; has {
			t.Fatalf("不该塞 thinking: %s", out)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "reasoning_effort") {
			t.Fatalf("只该有补 effort 一笔，实际 %v", notes)
		}
	})

	t.Run("没 thinking 没 tools → 不动", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("实测这个形态本来就 200，不该动: out=%s notes=%v", out, notes)
		}
	})

	t.Run("已设 effort → 只翻 thinking", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"disabled"},` +
			`"reasoning_effort":"high","messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
			t.Fatalf("thinking 应为 enabled，实际 %v", m["thinking"])
		}
		if m["reasoning_effort"] != "high" {
			t.Fatalf("客户端的 effort 不该被覆盖，实际 %v", m["reasoning_effort"])
		}
		if len(notes) != 1 {
			t.Fatalf("只该有一笔，实际 %v", notes)
		}
	})
}

// TestAlwaysThinksSkipsSwitchedModel 回归（2026-09-09 实抓 #12）：quirk 学
// 到 glm-5.3 之后，claude-bg 送分类器去 light 档（glm-4.5-air）的请求也被
// always-thinks 按 r.Model 的 quirk 翻回了 enabled+low——等于白切。补丁只能
// 打在学到它的那个模型身上。走全注册表（真实顺序）验证端到端结果。
func TestAlwaysThinksSkipsSwitchedModel(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "mid"
	r.LightProvider, r.LightModel = "smt-glm", "glm-4.5-air"
	body := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)

	res := Apply(body, r, nil)
	var m map[string]interface{}
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, res.Body)
	}
	if m["model"] != "glm-4.5-air" {
		t.Fatalf("分类器应切到 light，实际 %v", m["model"])
	}
	if r.Model != "glm-4.5-air" {
		t.Fatalf("框架没把上下文同步到切换后的模型（r.Model=%s）——下游会拿旧模型做决定", r.Model)
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("air 接受 disabled，thinking 不该被翻回: %v", m["thinking"])
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("不该有 reasoning_effort: %s", res.Body)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "always-thinks") {
			t.Fatalf("always-thinks 不该掺和切过模型的请求: %v", res.Notes)
		}
	}
}

// TestRegistrationOrder 组合语义靠注册顺序：意图层（claude-bg 表达「后台要
// 快」）→ 模型层补默认（deepseek/glm 填 disabled）→ 翻译层（always-thinks
// 把 disabled 翻成上游听得懂的话）必须最后。顺序来自同包内文件名字母序，
// 改名之前先想清楚这一层。
func TestRegistrationOrder(t *testing.T) {
	var names []string
	for _, p := range Plugins() {
		names = append(names, p.Name())
	}
	want := []string{"claude-bg", "deepseek", "glm", "always-thinks"}
	if len(names) != len(want) {
		t.Fatalf("注册表变了（%v），先把 want 更新成新事实再想是不是有意的", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("注册顺序错了: %v\n想要: %v", names, want)
		}
	}
}
