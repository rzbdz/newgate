package special

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/internal/core/domain"
)

// sysMarker 复刻实抓的分类器 system 开头（cc 2.1.263）。
const sysMarker = `"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}]`

func TestClaudeBgMatch(t *testing.T) {
	bg := func(agent string, stream bool, tier string) *Request {
		r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")
		r.Agent, r.Stream, r.Tier = agent, stream, tier
		return r
	}
	cases := []struct {
		name string
		r    *Request
		want bool
	}{
		{"claude 非流式 mid（分类器形态）", bg("claude", false, "mid"), true},
		{"claude 非流式 light", bg("claude", false, "light"), true},
		{"claude 非流式 heavy 也要管（tier 有歧义，见文件头）", bg("claude", false, "heavy"), true},
		{"claude 非流式没解析出 tier（兼容路径）", bg("claude", false, ""), true},
		{"claude 流式（主循环形态）", bg("claude", true, "mid"), false},
		{"opencode 非流式不碰", bg("opencode", false, "mid"), false},
		{"兼容路径（无 agent）不碰", bg("", false, "mid"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := (claudeBg{}).Match(c.r); got != c.want {
			t.Errorf("%s: Match=%v want %v", c.name, got, c.want)
		}
	}
}

// TestClaudeBgApply 现场形态：thinking 没写（dump 实抓 mid 请求就是 null）。
// 补显式 disabled；带了 adaptive 的也要改写；已是 disabled 的不动。
func TestClaudeBgApply(t *testing.T) {
	r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "mid"

	t.Run("没写 thinking 就补 disabled", func(t *testing.T) {
		body := []byte(`{"model":"mid","max_tokens":2112,"messages":[{"role":"user","content":"classify"}]}`)
		out, notes, err := (claudeBg{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("改完不是合法 JSON: %v", err)
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
			t.Fatalf("thinking 应为 disabled，实际 %v", m["thinking"])
		}
		if m["max_tokens"] != float64(2112) || m["model"] != "mid" {
			t.Fatalf("别的字段被动了: %v", m)
		}
		if len(notes) == 0 || !strings.Contains(notes[0], "disabled") {
			t.Fatalf("不静默原则：改了就得有 note，实际 %v", notes)
		}
	})

	t.Run("带了 adaptive 不碰（compact 总结显式要思考）", func(t *testing.T) {
		body := []byte(`{"model":"mid","thinking":{"type":"adaptive","budget_tokens":1024},"messages":[]}`)
		out, notes, err := (claudeBg{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("显式 thinking 不该被动（强改 disabled 会被始终思考模型拒掉）: out=%s notes=%v", out, notes)
		}
	})

	t.Run("已是 disabled 就不动", func(t *testing.T) {
		body := []byte(`{"model":"mid","thinking":{"type":"disabled"},"messages":[]}`)
		out, notes, err := (claudeBg{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("disabled 不该再动: out=%s notes=%v", out, notes)
		}
	})

	t.Run("reasoning_effort 时不注入（互斥）", func(t *testing.T) {
		body := []byte(`{"model":"mid","reasoning_effort":"low","messages":[]}`)
		out, notes, err := (claudeBg{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("设了推理强度不该注入: out=%s notes=%v", out, notes)
		}
	})
}

// TestRoutePlugin 改道是**路由决策**（建链之前，forward 在解析链前问这里）：
// 分类器 → 整条 light 链——含 fallback，不是只换链头。特征只认
// claude + 非流式 + system 标记；off 开关与 Apply 同源（st off 一起停）。
func TestRoutePlugin(t *testing.T) {
	classifier := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)
	summary := []byte(`{"model":"glm-5.3","max_tokens":2112,` +
		`"system":[{"type":"text","text":"Summarize this conversation."}],` +
		`"messages":[{"role":"user","content":"…transcript…"}]}`)

	cases := []struct {
		name   string
		agent  string
		stream bool
		body   []byte
		off    bool
		want   string
	}{
		{"分类器（claude 非流式 + 标记）→ light", "claude", false, classifier, false, "light"},
		{"其他后台调用（无标记）不改道", "claude", false, summary, false, ""},
		{"流式主循环不改道", "claude", true, classifier, false, ""},
		{"opencode 不碰", "opencode", false, classifier, false, ""},
		{"兼容路径（无 agent）不碰", "", false, classifier, false, ""},
		{"用户关掉 claude-bg → 改道一起停", "claude", false, classifier, true, ""},
		{"没有 system 字段", "claude", false, []byte(`{"model":"glm-5.3","messages":[]}`), false, ""},
		{"nil body", "claude", false, nil, false, ""},
	}
	for _, c := range cases {
		state := &domain.State{}
		if c.off {
			state.SpecialOff = []string{"claude-bg"}
		}
		decision, ok := Route(c.body, &Request{Agent: c.agent, Stream: c.stream}, state)
		got := ""
		if ok {
			got = decision.Tier
		}
		if got != c.want {
			t.Errorf("%s: Route=%q want %q", c.name, got, c.want)
		}
	}
}

func TestClaudeBgOwnsRouteStatusAndMetricMetadata(t *testing.T) {
	override := domain.Binding{Provider: "smt-deepseek", Model: "deepseek-flash"}
	state := &domain.State{ClassifierOverride: &override}
	items := Statuses(state)
	if len(items) != 1 || items[0].Label != "分类器覆盖" ||
		!strings.Contains(items[0].Value, override.String()) ||
		!strings.Contains(items[0].Value, "先于任何 profile") {
		t.Fatalf("插件状态不完整: %+v", items)
	}
	if hint, ok := MetricHint("special.claude-bg.route_light"); !ok ||
		!strings.Contains(hint, "light") {
		t.Fatalf("插件指标说明未注册: %q, %v", hint, ok)
	}
	bindings := Bindings(state)
	if len(bindings) != 1 || bindings[0] != override {
		t.Fatalf("插件 binding 未注册: %+v", bindings)
	}
}

func TestGlmMatch(t *testing.T) {
	cases := []struct {
		name string
		r    *Request
		want bool
	}{
		{"模型名 glm-5.3", req("glm-5.3", "smt-glm", "https://gw.example.com/v1"), true},
		{"模型名 glm-4.5-air", req("glm-4.5-air", "gw", "https://gw.example.com/v1"), true},
		{"provider 名", req("v5", "smt-glm", "https://gw.example.com/v1"), true},
		{"endpoint", req("v5", "gw", "https://open.bigmodel.cn/v1"), false}, // 无 "glm" 串
		{"deepseek 不归我管", req("deepseek-chat", "smt-deepseek", "https://gw.example.com/v1"), false},
		{"官方端点不碰", req("glm-5.3", "anth", "https://api.anthropic.com/v1"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := (glm{}).Match(c.r); got != c.want {
			t.Errorf("%s: Match=%v want %v", c.name, got, c.want)
		}
	}
}

// TestGlmApply 现场机制：glm 把「没写 thinking」当默认开。Claude Code 没写
// 就补显式 disabled；写了（真要思考）一个字节不动。
func TestGlmApply(t *testing.T) {
	r := claudeReq("glm-5.3")

	t.Run("没写 thinking 就补 disabled", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"go"}]}`)
		out, notes, err := (glm{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("改完不是合法 JSON: %v", err)
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
			t.Fatalf("thinking 应为 disabled，实际 %v", m["thinking"])
		}
		if len(notes) == 0 || !strings.Contains(notes[0], "GLM") {
			t.Fatalf("改了就得有 note，实际 %v", notes)
		}
	})

	t.Run("客户端写了 thinking 就不动（真要思考）", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"adaptive"},"messages":[]}`)
		out, notes, err := (glm{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("显式 thinking 不该被动: out=%s notes=%v", out, notes)
		}
	})

	t.Run("没有 messages 不动", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3"}`)
		out, notes, err := (glm{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("没 messages 没东西可补: out=%s notes=%v", out, notes)
		}
	})
}

// TestGlmApply_OpencodeUntouched 现场回归（2026-09-15，与 st-deepseek 同款）：
// opencode 走 OpenAI 方言，没有 thinking 这个字段可写，它不发 thinking 不是
// 「不想思考」。替它补 disabled 就是把用户给模型配的行为改掉。
func TestGlmApply_OpencodeUntouched(t *testing.T) {
	r := &Request{Model: "glm-5.3", Provider: "smt-glm",
		BaseURL: "https://gw.example.com/v1", Protocol: "openai",
		Path: "/chat/completions"}

	body := []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"go"}]}`)
	out, notes, err := (glm{}).Apply(body, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || len(notes) != 0 {
		t.Fatalf("opencode 的请求被补了 thinking（现场 bug）: out=%s notes=%v", out, notes)
	}
	// Match 仍然认领（它认的是上游）——认领但不动，这是两条独立的判断。
	if !(glm{}).Match(r) {
		t.Fatal("glm 插件该继续认领 glm 上游")
	}
}

// TestClaudeBgThenGlmCompose 组合语义：claude-bg 先把后台请求的 thinking
// 定为 disabled，glm/deepseek 看到已写就不动——两层各司其职，不重复注入。
func TestClaudeBgThenGlmCompose(t *testing.T) {
	r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "mid"
	body := []byte(`{"model":"mid","messages":[]}`)

	out1, _, err := (claudeBg{}).Apply(body, r)
	if err != nil {
		t.Fatal(err)
	}
	out2, notes, err := (glm{}).Apply(out1, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("claude-bg 已写 disabled，glm 不该再动: %v", notes)
	}
	if string(out1) != string(out2) {
		t.Fatalf("组合后 body 不该变化:\n%s\n%s", out1, out2)
	}
}
