package thinking

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/gateway/quirk"
	"github.com/rzbdz/newgate/go/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

const sysMarker = `"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}]`

type testClaudeBackground struct{}

func (testClaudeBackground) Name() string { return "claude-bg" }
func (testClaudeBackground) Why() string  { return "test fixture" }
func (testClaudeBackground) Before() []string {
	return []string{"deepseek", "glm", "always-thinks"}
}
func (testClaudeBackground) After() []string { return nil }
func (testClaudeBackground) Match(r *special.Request) bool {
	return r != nil && r.Agent == "claude" && !r.Stream
}
func (testClaudeBackground) Route(body []byte, r *special.Request, state *domain.State) (special.RouteDecision, bool) {
	if !(testClaudeBackground{}).Match(r) ||
		!bytes.Contains(body, []byte("You are a security monitor")) {
		return special.RouteDecision{}, false
	}
	return special.RouteDecision{Tier: "light"}, true
}
func (testClaudeBackground) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, r)
}

func init() {
	special.Register(testClaudeBackground{})
	special.Register(alwaysThinks{})
}

// markAlwaysThinks 模拟「转发时撞过一次 1210」之后的 quirk 注册表状态。
// 注册表是全局的，用完必须 Reset，别脏了别的测试。
func markAlwaysThinks(t *testing.T, provider, model string) {
	t.Helper()
	quirk.Reset()
	quirk.Mark(provider, model, quirk.NoThinkingDisable)
	t.Cleanup(quirk.Reset)
}

func req(model, provider, baseURL string) *special.Request {
	return &special.Request{
		Model: model, Provider: provider, BaseURL: baseURL,
		Protocol: "anthropic", Path: "/messages",
	}
}

func TestAlwaysThinksMatch(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	cases := []struct {
		name string
		r    *special.Request
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

// TestAlwaysThinksSkipsRoutedClassifier 回归（2026-09-09 实抓 #12 的路由版）：
// quirk 学到 glm-5.3 之后，分类器请求必须**整个改道 light 链**——Route
// 在建链之前决定，forward 的链循环把 body 的 model 换成 light 链头
// （glm-4.5-air）之后 special 才看到它。于是 glm-5.3 的 quirk
// （NoThinkingDisable）从头到尾没机会掺和：thinking 保持 disabled、
// 不补 reasoning_effort。走全注册表按真实顺序验证端到端结果。
func TestAlwaysThinksSkipsRoutedClassifier(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	body := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)

	// 1) 路由层：分类器改道 light（forward 在解析链之前问这里）
	if decision, ok := special.Route(body, &special.Request{Agent: "claude"}, &domain.State{}); !ok || decision.Tier != "light" {
		t.Fatalf("分类器应改道 light 链，实际 %+v", decision)
	}
	// 2) 链循环：body 的 model 换成 light 链头
	body, err := rewrite.ReplaceTopLevelString(body, "model", "glm-4.5-air")
	if err != nil {
		t.Fatal(err)
	}
	// 3) special 层：r.Model 从一开始就是 air
	r := req("glm-4.5-air", "smt-glm", "https://x/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "light"

	res := special.Apply(body, r, nil)
	var m map[string]interface{}
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, res.Body)
	}
	if m["model"] != "glm-4.5-air" {
		t.Fatalf("模型应保持 light 链头，实际 %v", m["model"])
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("air 接受 disabled，thinking 不该被 glm-5.3 的 quirk 翻回: %v", m["thinking"])
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("不该有 reasoning_effort: %s", res.Body)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "always-thinks") {
			t.Fatalf("always-thinks 不该掺和改道后的请求: %v", res.Notes)
		}
	}
}

// TestBestEffortDisableThinkDumbLightConfig 错配现场（2026-09-09 用户提的
// 场景）：用户把「不支持关思考」的模型配进了 light 档。best effort 的含义
// 就在这里——意图照样落地，模型听不懂就翻成它听得懂的最小思考，请求
// 活着（慢就慢，由他去了），绝不因为关不掉而失败。
func TestBestEffortDisableThinkDumbLightConfig(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	body := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)

	// 路由照走 light（错配下 light 链头就是 glm-5.3 本尊）
	if decision, ok := special.Route(body, &special.Request{Agent: "claude"}, &domain.State{}); !ok || decision.Tier != "light" {
		t.Fatalf("错配不改路由决策，实际 %+v", decision)
	}
	// light 链头：r.Model = glm-5.3；BestEffortDisableThink 一个操作完成
	// 意图落地 + 翻译
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "light"

	res := special.Apply(body, r, nil) // 走全注册表：claude-bg → always-thinks
	var m map[string]interface{}
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, res.Body)
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
		t.Fatalf("不支持关思考的模型应被翻译成 enabled，实际 %v", m["thinking"])
	}
	if m["reasoning_effort"] != "low" {
		t.Fatalf("应补 reasoning_effort=low，实际 %v", m["reasoning_effort"])
	}
}

// TestBestEffortDisableThinkModelGuard 翻译按 (provider, r.Model) 的 quirk
// 决定：body 已被切到别的模型时不掺和（写意图不受影响——那是模型无关的）。
func TestBestEffortDisableThinkModelGuard(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	// r.Model 还是 glm-5.3（quirk 在它身上），但 body 的 model 已是 air
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	body := []byte(`{"model":"glm-4.5-air","messages":[]}`)

	out, notes, err := BestEffortDisableThink(body, r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(out, &m)
	// 意图落地了（模型无关）……
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("写意图不受模型守卫影响，实际 %v", m["thinking"])
	}
	// ……但 glm-5.3 的 quirk 没有翻它（air 不背这口锅）
	if m["reasoning_effort"] != nil {
		t.Fatalf("不该按 glm-5.3 的 quirk 给 air 补 effort: %s", out)
	}
	for _, n := range notes {
		if strings.Contains(n, "always-thinks") || strings.Contains(n, "不支持关闭思考") {
			t.Fatalf("翻译不该发生: %v", notes)
		}
	}
}

// TestRegistrationOrder 组合语义靠显式优先级，而不是文件名或 init 顺序。
func TestRegistrationOrder(t *testing.T) {
	var names []string
	for _, p := range special.Plugins() {
		names = append(names, p.Name())
	}
	want := []string{"claude-bg", "always-thinks"}
	if len(names) != len(want) {
		t.Fatalf("注册表变了（%v），先把 want 更新成新事实再想是不是有意的", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("注册顺序错了: %v\n想要: %v", names, want)
		}
	}
}
