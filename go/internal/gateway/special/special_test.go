package special

import (
	"encoding/json"
	"strings"
	"testing"
)

func req(model, provider, baseURL string) *Request {
	return &Request{
		InModel: "heavy", Tier: "heavy",
		Model: model, Provider: provider, BaseURL: baseURL,
		Protocol: "anthropic", Path: "/messages",
	}
}

func TestDeepseekMatch(t *testing.T) {
	cases := []struct {
		name string
		r    *Request
		want bool
	}{
		{"模型名", req("deepseek-chat", "gw", "https://gw.example.com/v1"), true},
		{"模型名大写", req("DeepSeek-V3", "gw", "https://gw.example.com/v1"), true},
		{"provider 名", req("v3-1", "deepseek", "https://gw.example.com/v1"), true},
		{"endpoint", req("v3-1", "gw", "https://api.deepseek.com/v1"), true},
		{"无关模型", req("claude-sonnet-4", "anth", "https://gw.example.com/v1"), false},
		{"官方端点不碰", req("deepseek-chat", "anth", "https://api.anthropic.com/v1"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := (deepseek{}).Match(c.r); got != c.want {
			t.Errorf("%s: Match=%v want %v", c.name, got, c.want)
		}
	}
}

// TestDeepseekApply 复现现场那条 400：多轮里带 tool_use 的 assistant 消息
// 没有 reasoning_content。两处修补都要落地，其他字节一个不动。
func TestDeepseekApply(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","max_tokens":8192,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"看下这个文件","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Read","input":{"path":"a.go"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"…"}]}` +
		`],"stream":true}`)

	out, notes, err := (deepseek{}).Apply(body, req("deepseek-chat", "gw", "https://gw.example.com/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("应该有两条改动记录，实际 %v", notes)
	}

	var v struct {
		Model    string `json:"model"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Messages []map[string]json.RawMessage `json:"messages"`
		Stream   bool                         `json:"stream"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("结果不是合法 JSON: %v\n%s", err, out)
	}
	if v.Thinking.Type != "disabled" {
		t.Fatalf(`thinking 没被设成 disabled: %s`, out)
	}
	if v.Model != "deepseek-chat" || !v.Stream {
		t.Fatal("其他顶层字段被改坏了")
	}
	for i, m := range v.Messages {
		var role string
		_ = json.Unmarshal(m["role"], &role)
		rc, has := m["reasoning_content"]
		if role == "assistant" {
			if !has || string(rc) != `""` {
				t.Fatalf("第 %d 条 assistant 缺 reasoning_content: %s", i, out)
			}
		} else if has {
			t.Fatalf("第 %d 条 %s 被误补了 reasoning_content", i, role)
		}
	}
	// cache_control 这类客户端语义必须逐字节存活
	if !strings.Contains(string(out), `"cache_control":{"type":"ephemeral"}`) {
		t.Fatal("cache_control 没有原样保留")
	}
}

// TestDeepseekApply_RespectsExplicitThinking 用户自己写了 thinking 就不覆盖：
// 显式要思考模式时我们无权悄悄关掉。
func TestDeepseekApply_RespectsExplicitThinking(t *testing.T) {
	body := []byte(`{"model":"deepseek-reasoner","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[{"role":"assistant","content":"hi"}]}`)
	out, notes, err := (deepseek{}).Apply(body, req("deepseek-reasoner", "gw", "https://x/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"type":"disabled"`) {
		t.Fatalf("覆盖了用户显式设置的 thinking: %s", out)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "reasoning_content") {
		t.Fatalf("应该只补 reasoning_content，实际 %v", notes)
	}
}

// TestDeepseekApply_NoMessages 没有 messages（如 /v1/models）不算错。
func TestDeepseekApply_NoMessages(t *testing.T) {
	out, notes, err := (deepseek{}).Apply([]byte(`{"model":"deepseek-chat"}`),
		req("deepseek-chat", "gw", "https://x/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("只该注入 thinking，实际 %v", notes)
	}
	if !strings.Contains(string(out), `"thinking":{"type":"disabled"}`) {
		t.Fatalf("got %s", out)
	}
}

// TestDeepseekApply_Idempotent 跑第二遍不该再改任何东西——fallback 链重试同一个
// 请求体时不能越改越多。
func TestDeepseekApply_Idempotent(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"a"}]}`)
	r := req("deepseek-chat", "gw", "https://x/v1")
	once, _, err := (deepseek{}).Apply(body, r)
	if err != nil {
		t.Fatal(err)
	}
	twice, notes, err := (deepseek{}).Apply(once, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("第二遍还在改: %v", notes)
	}
	if string(twice) != string(once) {
		t.Fatalf("不幂等:\n%s\n%s", once, twice)
	}
}

// ---- 框架本身 ----

func TestApply_SkipsNonMatching(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"assistant","content":"a"}]}`)
	res := Apply(body, req("claude-sonnet-4", "anth", "https://api.anthropic.com/v1"), nil)
	if res.Changed {
		t.Fatalf("不该改 Anthropic 的请求: %s", res.Body)
	}
	if string(res.Body) != string(body) {
		t.Fatal("body 被动了")
	}
}

func TestApply_HonorsOffSwitch(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"a"}]}`)
	r := req("deepseek-chat", "gw", "https://x/v1")

	if res := Apply(body, r, nil); !res.Changed {
		t.Fatal("默认应该生效")
	}
	off := func(name string) bool { return name == "deepseek" }
	res := Apply(body, r, off)
	if res.Changed || string(res.Body) != string(body) {
		t.Fatalf("关掉了还在改: %s", res.Body)
	}
}

// TestApply_FailOpen 插件报错时请求必须原样发出去，而且要留下痕迹。
func TestApply_FailOpen(t *testing.T) {
	saved := registry
	defer func() { registry = saved }()
	registry = []Plugin{boom{}}

	body := []byte(`{"model":"x"}`)
	res := Apply(body, req("x", "y", "z"), nil)
	if res.Changed || string(res.Body) != string(body) {
		t.Fatal("插件报错后 body 不该变")
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "boom") {
		t.Fatalf("报错没被记录: %v", res.Notes)
	}
}

type boom struct{}

func (boom) Name() string        { return "boom" }
func (boom) Why() string         { return "测试用" }
func (boom) Match(*Request) bool { return true }
func (boom) Apply(b []byte, r *Request) ([]byte, []string, error) {
	return []byte(`{"毁了":true}`), []string{"改了"}, errFake
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "故意失败" }

// TestPluginsHaveWhy 每个插件都必须能说清自己为什么存在——将来判断
// 「上游修好了没、这段还要不要」全靠这句话。
func TestPluginsHaveWhy(t *testing.T) {
	ps := Plugins()
	if len(ps) == 0 {
		t.Fatal("一个插件都没注册")
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Name() == "" || len(p.Why()) < 10 {
			t.Errorf("插件 %q 的 Name/Why 不合格", p.Name())
		}
		if seen[p.Name()] {
			t.Errorf("插件名重复: %s", p.Name())
		}
		seen[p.Name()] = true
	}
}
