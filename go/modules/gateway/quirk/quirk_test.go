package quirk

import (
	"testing"
)

// Learn 只认**实测复现过**的报错原文。这个测试逐个锁住签名表里的每一条：
// 少一条 = 那条上游的毛病永远学不到，每次请求都重新撞一遍 400。
func TestLearnRecognizesEverySignature(t *testing.T) {
	// 每条都来自真实 dump，不是编的。
	tests := []struct {
		name   string
		body   string
		reason string
	}{
		{
			"GLM 1210",
			`{"error":{"message":"[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。]"}}`,
			"dump/err-400-req000082、req000117（route: mid -> smt-glm/glm-5.3-flash）",
		},
		{
			"kimi：only type=enabled is allowed",
			`{"error":{"type":"<nil>","message":"invalid thinking: only type=enabled is allowed for this model (request id: 202609170742120)"}}`,
			"dump/err-400-req000213、req000230（route: mid -> kimi/kimi-k2.7-code）——" +
				"2026-09-17 之前这条措辞一条签名都不匹配，所以永远学不到",
		},
		{
			"deepseek：cannot be disabled",
			`{"error":{"message":"deepseek thinking options type cannot be disabled"}}`,
			"措辞变体",
		},
		{
			"does not support disabling thinking",
			`{"error":{"message":"this model does not support disabling thinking"}}`,
			"措辞变体",
		},
		{
			"thinking cannot be turned off",
			`{"error":{"message":"Thinking cannot be turned off for this model"}}`,
			"措辞变体；大小写不敏感",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Reset()
			got := Learn("prov", "model-x", 400, []byte(tt.body))
			if len(got) != 1 || got[0] != "该模型始终思考" {
				t.Fatalf("没学到（%s）: %v", tt.reason, got)
			}
			if !Has("prov", "model-x", NoThinkingDisable) {
				t.Fatal("学到了却没打上 flag")
			}
			// 幂等：同一个毛病第二次不再重复报。
			if again := Learn("prov", "model-x", 400, []byte(tt.body)); again != nil {
				t.Fatalf("重复学了一次: %v", again)
			}
		})
	}
}

// 认不出的一律不猜：宁可让用户看到原始报错，也不能瞎给请求加字段。
func TestLearnNeverGuesses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"无关的 400", 400, `{"error":{"message":"invalid request: max_tokens too large"}}`},
		{"5xx 是上游自己挂了，与请求形状无关", 500, `{"error":{"message":"始终思考"}}`},
		{"429", 429, `{"error":{"message":"rate limit"}}`},
		{"空 body", 400, ``},
		{"只有一句普通的话，不含任何签名", 400, `{"error":{"message":"bad request"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Reset()
			if got := Learn("prov", "model-y", tt.status, []byte(tt.body)); got != nil {
				t.Fatalf("不该学到却学到了: %v", got)
			}
			if Has("prov", "model-y", NoThinkingDisable) {
				t.Fatal("不该打 flag 却打了")
			}
		})
	}
}

// 学习是按 (provider, model) 记账的：给 A 学到的东西不能漏给 B，也不能串到 B。
func TestLearnIsScopedToProviderAndModel(t *testing.T) {
	Reset()
	Learn("kimi", "kimi-k2.7-code", 400, []byte(`only type=enabled is allowed`))
	if !Has("kimi", "kimi-k2.7-code", NoThinkingDisable) {
		t.Fatal("自己没学到")
	}
	if Has("kimi", "kimi-k2.7", NoThinkingDisable) || Has("glm", "kimi-k2.7-code", NoThinkingDisable) {
		t.Fatal("串到别的 provider/model 上了")
	}
}

// Snapshot 要能看见已经学到的 flag（CLI/诊断读它）。
func TestSnapshotShowsLearnedFlags(t *testing.T) {
	Reset()
	Learn("kimi", "kimi-k2.7-code", 400, []byte(`only type=enabled is allowed`))
	found := false
	for _, e := range Snapshot() {
		if e.Provider == "kimi" && e.Model == "kimi-k2.7-code" &&
			e.Flags&NoThinkingDisable != 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("快照里看不到刚学到的 flag: %v", Snapshot())
	}
}
