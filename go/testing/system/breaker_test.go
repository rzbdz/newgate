package system_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/testing/system"
)

// strictReasoningBody 造一发**会被假上游按实测口径拒掉**的请求，也就是线上那条
// 「reasoning_content must be passed back」400 的真形态。
//
// 判据是**尾部形状**（2026-09-18 按实测重写；判据本体在 testing/upstream/dialect.go
// 的 strictReasoningViolation，它是 mock/fake_upstream.py 的逐字替身）：
//
//	最后一条 role:user 消息的 content[] 非空、且里面**全是** tool_result 块 → 400
//
// 这里就是 Claude Code 主循环的标准形状：模型调了工具，客户端把结果送回去等它接着
// 干，那个 content[] 里一个文字块都没有。**跟推理字段无关**——实测把 reasoning_content
// 换成真实原文 / 省略 / 空串 / 占位符，这一格全是 400；同一个请求体只在那个 content[]
// 里加一个空格，就变 200。
//
// 必须 stream=true：非流式的 /a/claude/ 请求会被 claudecode 的 claude-bg 插件补上
// thinking:disabled，那是另一条路（虽然实测形状校验也不吃 thinking，但主循环的真实
// 形态本来就是流式）。deepseek 插件不认这个 provider（链上是 upstream-a），所以
// 尾部修复不会介入——发出去的就是这里写的字节。
func strictReasoningBody() string {
	return `{"model":"normal","max_tokens":16,"stream":true,` +
		`"tools":[{"name":"Bash","description":"run a command",` +
		`"input_schema":{"type":"object","properties":{}}}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"跑一下"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_strict_1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_strict_1","content":"ok"}]}]}`
}

// TestDeepSeekShapeDetectorIsWiredThroughTheRealGraph 是 2026-09-17 那次事故的
// **集成级**回归：整张真组件图 + 真转发 + 假上游，deepseek 模块在 Start 里注册
// 的形状判据必须真的生效。
//
// 为什么这条测试必须走整张图，而不是再写一个 forward 的单测：现场坏的是**接线**
// ——判据住在 modules/deepseek，注册进的是 modules/breaker 的健康表，而健康表是
// **注入**给 forward 的（forward.New 的第四个参数）。任何一处接错（模块没 Need
// breaker、注册句柄没在 Stop 释放、装配清单漏了 deepseek、harness 自己
// newTable() 而不是用图里那张），单测都照样绿、线上照样把 deepseek 摘掉。
// 所以这里断言的是端到端的因果链：
//
//	假上游回严格 400 → 健康表里 deepseek 的判据认领 → 只计数不摘牌 → 日志说明
//
// 现场（~/.config/newgate/health.json + 日志）：deepseek-flash 因这条 400 被连续
// 两发数到阈值摘掉，而 `newgate probe` 一直是 fluent——探活发的是最小请求，永远
// 触发不到「思考模式要求逐字回传」。摘牌的代价不对称：用户被悄悄换给别的模型，
// 还要等 60s 起的冷却。
func TestDeepSeekShapeDetectorIsWiredThroughTheRealGraph(t *testing.T) {
	h := system.Start(t)

	// 每个候选都回同一份严格 400。形状错误**会**继续沿链试（同一份 body 换个
	// 校验更松的 provider 有可能收下），所以一发客户端请求打两次上游——
	// cheap.mid 的链是 upstream-a/model-medium → upstream-b/model-medium。
	const hitsPerRequest = 2

	for i := 0; i < 3; i++ {
		resp := h.Post("/a/claude/v1/messages", strictReasoningBody())
		body := system.ReadBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("第 %d 发状态 = %d, want 400（上游原文必须原样透传）；body=%s",
				i+1, resp.StatusCode, body)
		}
		if !strings.Contains(body, "must be passed back") {
			t.Fatalf("第 %d 发没把上游原文转给客户端（不静默是硬要求）；body=%s", i+1, body)
		}
	}

	if got := h.Upstream.Count(); got != 3*hitsPerRequest {
		t.Fatalf("假上游共收到 %d 发，want %d（链上两个候选各试一次）",
			got, 3*hitsPerRequest)
	}

	// 判据认领的**证据**在日志里：转发路径不认识任何上游专有字符串，它只读
	// Result.Shape 那个名字，所以名字必须被打出来，否则人手里什么都查不到。
	if !h.WaitForLog(t, "[shape-400]") {
		t.Fatalf("日志里没有 [shape-400]——判据没被认领，或转发路径没读 Shape；\n%s",
			h.Logs())
	}
	if !strings.Contains(h.Logs(), "判据 deepseek") {
		t.Fatalf("日志没说清是哪条判据认的（多家上游同时报 400 时这是唯一能分辨的信息）；\n%s",
			h.Logs())
	}

	// 核心断言：账本纹丝不动。Open=true 意味着用户的下一发被换给了别人。
	skips := 0
	for _, s := range h.Breaker().Snapshot() {
		skips += s.ShapeSkips
		if s.ShapeSkips == 0 {
			continue
		}
		if s.ShapeSkips != 3 {
			t.Errorf("%s 的 shape_skips = %d, want 3（每发客户端请求各记一次）",
				s.Provider+"/"+s.Model, s.ShapeSkips)
		}
		if s.Fails != 0 || s.Open || s.State != "closed" {
			t.Errorf("%s 被请求形状错误摘掉了：fails=%d open=%v state=%s reason=%q——"+
				"这类 400 换哪个 provider 都一样，不该记在任何一家的账上",
				s.Provider+"/"+s.Model, s.Fails, s.Open, s.State, s.Reason)
		}
	}
	if skips != 3*hitsPerRequest {
		t.Errorf("全部 binding 的 shape_skips 合计 = %d, want %d——判据没生效时是 0，"+
			"那意味着这类 400 又落进了可用性账本（阈值 2，第一发就该摘牌）",
			skips, 3*hitsPerRequest)
	}
}

// TestOtherClientErrorIsNotClaimedAsShape 是上一条的对照组：判据必须**会挑**。
//
// 一条见 400 就认领的判据同样能让上一条变绿，但它把「上游真的在拒我们的请求」
// 一起放过了。形状判据存在的意义恰恰是那个区分：只有「同一份 body 换个 provider
// 也会错」才叫请求形状问题，其余的 400 是上游在说「你这份请求不对」。
//
// 用 FailNext 而不是换个请求体：同一个请求形状、只有上游的回应不同——变量唯一。
// 非形状 400 既不记账也不换站（换站由 chain.fallback_on_400 单独表达），所以链
// 停在第一个候选上，客户端拿到的就是那一发的结果。
func TestOtherClientErrorIsNotClaimedAsShape(t *testing.T) {
	h := system.Start(t)

	h.Upstream.FailNext(http.StatusBadRequest)
	resp := h.Post("/a/claude/v1/messages", strictReasoningBody())
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态 = %d, want 400；body=%s", resp.StatusCode, body)
	}
	if got := h.Upstream.Count(); got != 1 {
		t.Errorf("假上游被打了 %d 发，want 1——非形状 400 不该沿链换人撞遍所有上游", got)
	}
	if strings.Contains(h.Logs(), "[shape-400]") {
		t.Fatalf("与 reasoning 无关的 400 被判成了请求形状错误：判据太宽，会把真正的"+
			"可用性故障一起放过；\n%s", h.Logs())
	}
	for _, s := range h.Breaker().Snapshot() {
		if s.ShapeSkips != 0 || s.Fails != 0 || s.Open {
			t.Errorf("%s 因为一发与它无关的 400 进了账本：%+v", s.Provider+"/"+s.Model, s)
		}
	}
}
