package special

import (
	"encoding/json"
	"strings"
	"testing"
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
		{"claude 非流式 heavy（点名要大模型）不碰", bg("claude", false, "heavy"), false},
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

	t.Run("带了 adaptive 也改写（客户端设置泄漏）", func(t *testing.T) {
		body := []byte(`{"model":"mid","thinking":{"type":"adaptive","budget_tokens":1024},"messages":[]}`)
		out, notes, err := (claudeBg{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
			t.Fatalf("adaptive 应回改写为 disabled，实际 %v", m["thinking"])
		}
		if len(notes) == 0 || !strings.Contains(notes[0], "改写") {
			t.Fatalf("改写必须有 note，实际 %v", notes)
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

// TestClaudeBgLightSwitch 切轻档只认分类器本体：system 里那句自报家门
// （"You are a security monitor…"）是实抓特征；其他后台调用（总结、起
// 标题）没有这句，保留 mid 的体格。跨 provider 不切——那得动路由。
func TestClaudeBgLightSwitch(t *testing.T) {
	base := func() *Request {
		r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")
		r.Agent, r.Stream, r.Tier = "claude", false, "mid"
		r.LightProvider, r.LightModel = "smt-glm", "glm-4.5-air"
		return r
	}
	classifier := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)
	summary := []byte(`{"model":"glm-5.3","max_tokens":2112,` +
		`"system":[{"type":"text","text":"Summarize this conversation."}],` +
		`"messages":[{"role":"user","content":"…transcript…"}]}`)

	t.Run("分类器（带标记）：切 light + 禁思考", func(t *testing.T) {
		r := base()
		out, notes, err := (claudeBg{}).Apply(classifier, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("改完不是合法 JSON: %v", err)
		}
		if m["model"] != "glm-4.5-air" {
			t.Fatalf("model 应切到 light，实际 %v", m["model"])
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
			t.Fatalf("thinking 应为 disabled，实际 %v", m["thinking"])
		}
		if len(notes) != 2 || !strings.Contains(notes[0], "glm-4.5-air") ||
			!strings.Contains(notes[0], "分类器") || !strings.Contains(notes[1], "disabled") {
			t.Fatalf("两笔改动都要有 note，实际 %v", notes)
		}
	})

	t.Run("其他后台调用（无标记）：保留 mid，只禁思考", func(t *testing.T) {
		r := base()
		out, notes, err := (claudeBg{}).Apply(summary, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if m["model"] != "glm-5.3" {
			t.Fatalf("非分类器不该切模型，实际 %v", m["model"])
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
			t.Fatalf("thinking 照禁，实际 %v", m["thinking"])
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "disabled") {
			t.Fatalf("只该有禁思考一笔，实际 %v", notes)
		}
	})

	t.Run("跨 provider：不切", func(t *testing.T) {
		r := base()
		r.LightProvider = "other-gw"
		out, _, err := (claudeBg{}).Apply(classifier, r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "glm-4.5-air") {
			t.Fatal("跨 provider 不该切")
		}
	})

	t.Run("light 没配置：不切", func(t *testing.T) {
		r := base()
		r.LightProvider, r.LightModel = "", ""
		out, notes, err := (claudeBg{}).Apply(classifier, r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "glm-4.5-air") {
			t.Fatal("没配置 light 不该切")
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "thinking") {
			t.Fatalf("只该有 thinking 一笔，实际 %v", notes)
		}
	})

	t.Run("已经是 light：只剩禁思考一笔", func(t *testing.T) {
		r := base()
		r.Tier = "light"
		r.Model = "glm-4.5-air"
		already := []byte(`{"model":"glm-4.5-air","max_tokens":100,` + sysMarker +
			`,"messages":[{"role":"user","content":"x"}]}`)
		out, notes, err := (claudeBg{}).Apply(already, r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"model":"glm-4.5-air"`) {
			t.Fatalf("模型不该再动: %s", out)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "thinking") {
			t.Fatalf("模型没变不该有切档 note，实际 %v", notes)
		}
	})
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

// TestGlmApply 现场机制：glm 把「没写 thinking」当默认开。没写就补显式
// disabled；写了（真要思考）一个字节不动。
func TestGlmApply(t *testing.T) {
	r := req("glm-5.3", "smt-glm", "https://gw.example.com/v1")

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
