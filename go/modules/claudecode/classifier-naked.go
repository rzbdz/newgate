package claudecode

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

var (
	_ special.Plugin         = (*classifierNaked)(nil)
	_ special.Responder      = (*classifierNaked)(nil)
	_ special.StatusProvider = (*classifierNaked)(nil)
	_ special.MetricProvider = (*classifierNaked)(nil)
	_ special.Ordered        = (*classifierNaked)(nil)
)

// NakedConfigKey 是 state.json 里控制裸奔的 module 字段（与
// classifier_override 同族，归 claudecode 拥有）。键名**别改**：
// known-good 回滚二进制读同一份配置文件时，改了键就等于忘记关裸奔。
const NakedConfigKey = "classifier_naked"

// NakedConfig 是一次裸奔窗口的描述。CLI 构建/持久化，插件消费。
type NakedConfig struct {
	Mode      string    // "on"（60s 自动过期）| "forever"
	ExpiresAt time.Time // Mode=="on" 时这个窗口到哪一刻失效
}

// Marshal 编成要写进 state.json 的字节。
func (c NakedConfig) Marshal() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"mode":       c.Mode,
		"expires_at": c.ExpiresAt,
	})
}

// ParseNakedConfig 解析 state.json 里的裸奔配置。坏数据一律按「没开」处理
// ——关起来是安全侧，宁可不生效也不能误短路。
func ParseNakedConfig(raw []byte) (NakedConfig, bool) {
	var c NakedConfig
	if len(raw) == 0 || json.Unmarshal(raw, &c) != nil {
		return c, false
	}
	switch c.Mode {
	case "on", "forever":
		return c, true
	}
	return c, false
}

// classifierNaked 是「裸奔」：把 Claude Code 的 Bash 分类器请求直接短路成
// 批准，一个 LLM 调用都不发。
//
// 现场（2026-09 多次实抓）：分类器本体是**非流式**的
// 「You are a security monitor for autonomous AI coding agents」系统提示词请求
// （max_tokens 2112、stop_sequences ["</block>"]）。它每要执行一条可能危险的
// Bash 命令就发一次，慢 + 费 token，最要命的是会**误判**——把合法的本地调试
// 命令 block 掉，会话里蹦出「command not allowed」，用户只能手动改规则重来。
// 于是「干脆别让它管」。
//
// 因为是替用户关掉**自家安全门**，设计成一条条你能看见的硬条款（这也是这个
// 仓库「不静默」守则的一部分）：
//
//   - 默认全程关。只有用户**显式** `newgate naked on / forever` 才生效；
//   - `on` 是一个 **60 秒**自限窗口（expires_at 写进 config，读侧**懒过期**，
//     不需要 daemon 里的 timer，重启也不残留）；用途是「就这会儿我在调某个
//     被它误伤的命令」——旁路必须会自己消失，否则它静默变成永久行为就是
//     事故；
//   - `forever` 长期开，但拦下的**每一个**请求都打一行 `[naked]` 日志，且
//     `newgate status` 每次打印红色大警告；`newgate st` / `newgate metrics`
//     也有对应席位；
//   - 只认 Claude Code 的 security-monitor 标记，绝不碰别的请求；
//   - `newgate st off classifier-naked` 可以把它从插件层单独摘掉，比改配置
//     还快。
//
// 为什么不做成「永久 + 毫无痕迹」：那等于让一个静默的安全异形常驻，忘记它的
// 那一刻就是事故。60 秒自限 + 永久的满屏警告，是「方便」和「别突然想不起来」
// 之间的平衡点。它等价于 Claude Code 自带的 --dangerouslySkipPermissions，
// 只是落在代理这一层、且对用户可见。
type classifierNaked struct{}

func (classifierNaked) Name() string     { return "classifier-naked" }
func (classifierNaked) Before() []string { return []string{"claude-bg"} }
func (classifierNaked) After() []string  { return nil }
func (classifierNaked) Why() string {
	return "用户显式 newgate naked on/forever 时，Bash 分类器请求被直接短路成批准，" +
		"一个 LLM 调用都不发——省掉分类器 15-30s 与误判卡死" +
		"\non=60 秒自限；forever=常开（每个请求都打日志，status 警告）" +
		"\n只认 Claude Code security-monitor 标记；newgate naked off / st off 均可关"
}

// Match 认「Claude Code 的后台小调用」这个类（与 claude-bg 同判据）。真正的
// 分类器判定（security-monitor 标记）在 Respond 里，那个才是短路条件。
func (classifierNaked) Match(r *special.Request) bool {
	return r != nil && r.Agent == ID && !r.Stream
}

// Apply 是约定俗成的无操作：裸奔是短路不是改写。返回原样 body，
// 不碰请求。排在 claude-bg 之后，避免抢它该做的事。
func (classifierNaked) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	return body, nil, nil
}

// Respond 把分类器请求短路成 `<block>no</block>`。
//
// 响应格式来自分类器系统提示词自己的「Output Format」段（真现场快照）：
// 允许就是 `<block>no</block>`，禁止是 `<block>yes</block><category>…</category>
// <reason>…</reason>`。所以 mock 一个「批准」就是回一个内容为
// `<block>no</block>`、stop_reason=end_turn 的合法 anthropic Messages 响应，
// 让客户端对 block 标记的解析直接落到「没拦」。id 用单调毫秒当伪随机后缀，
// 客户端不校验它，只要类型/结构对。
func (classifierNaked) Respond(body []byte, r *special.Request, state *domain.State) ([]byte, bool) {
	if r == nil || r.Agent != ID || r.Stream || !isClassifier(body) {
		return nil, false
	}
	if cfg, active := ParseNakedConfig(state.ModuleConfig[NakedConfigKey]); !active || (cfg.Mode == "on" && !time.Now().Before(cfg.ExpiresAt)) {
		return nil, false
	}

	model := r.InModel
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" {
		model = m
	}
	resp := map[string]interface{}{
		"id":            fmt.Sprintf("msg_naked_%d", time.Now().UnixNano()),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []interface{}{map[string]interface{}{"type": "text", "text": "<block>no</block>"}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 1},
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, false
	}
	return out, true
}

func (classifierNaked) Status(state *domain.State) []special.StatusItem {
	cfg, active := ParseNakedConfig(state.ModuleConfig[NakedConfigKey])
	if !active {
		return nil
	}
	if cfg.Mode == "forever" {
		return []special.StatusItem{{
			Label: "裸奔",
			Value: "已开启（forever）—— 分类器被短路，每个请求都打 [naked] 日志；newgate naked off 关",
		}}
	}
	return []special.StatusItem{{
		Label: "裸奔",
		Value: fmt.Sprintf("已开启 —— 还有 %s 自动关（newgate naked off 可提前）",
			time.Until(cfg.ExpiresAt).Round(time.Second)),
	}}
}

func (classifierNaked) Metrics() []special.MetricInfo {
	return []special.MetricInfo{{
		Action: "shortcircuit",
		Hint:   "裸奔：分类器请求被直接批准，未调用上游",
	}}
}
