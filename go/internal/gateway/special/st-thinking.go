package special

import (
	"github.com/rzbdz/newgate/go/internal/gateway/quirk"
	"github.com/rzbdz/newgate/go/internal/gateway/rewrite"
)

func init() { Register(alwaysThinks{}) }

// alwaysThinks 修「这个模型始终思考，不支持关闭思考」这一类 400。
//
// 现场报错（智谱 GLM，code 1210）
//
//	[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。][2026…]
//
// 实测复现（api.rvcompute.com 聚合器 + glm-5.3）：
//
//	tools + 不给 reasoning_effort       → 400 / 1210
//	tools + reasoning_effort=low|high   → 200
//	thinking:{"type":"disabled"}        → 400 / 1210
//	不带 tools、也不带 thinking          → 200
//	同样带 tools 的 glm-5.2 / glm-4.7   → 200
//
// 成因：聚合器看见 tools 就替我们给上游塞了「关闭思考」（很多模型不支持
// 思考+工具同时用），而 glm-5.3 是始终思考的模型，直接拒。改不了聚合器，
// 但只要我们**显式**给一个思考强度，就能盖过它塞的那个值。
//
// 补什么：
//
//  1. thinking 是 disabled 的话改成 enabled —— 这个模型压根不能关，
//     留着 disabled 必定 400。
//  2. 没有 reasoning_effort 就补 "low" —— 报错原文点名要 low/high/max，
//     其中 low 最接近客户端「别想太多」的本意（实测 none 也收，但报错
//     原文没提它，不赌）。
//
// 与 deepseek 插件的关系：它注册在前，可能刚给请求塞了
// thinking:{"type":"disabled"}；这里在后面把它改回来。顺序是靠文件名
// 保证的（st-always-thinks.go 在 st-deepseek.go 之前会出问题——所以这个
// 文件必须排在它后面，见下面的断言测试）。
//
// 为什么 Match 只信 quirk 注册表、不按模型名猜：「始终思考」是模型版本的
// 属性，会变，也没接口能查。按名字猜会给一堆无关请求乱加字段，比不修更糟。
// 注册表的两个来源：转发时撞上这个报错学到（一次失败换永久免疫），或者
// newgate probe 主动探出来。
type alwaysThinks struct{}

func (alwaysThinks) Name() string { return "always-thinks" }

func (alwaysThinks) Why() string {
	return "有些模型始终思考，收到「关闭思考」就 400（GLM code 1210）\n" +
		"给这些模型补显式 reasoning_effort=low，并把 thinking:disabled 改回 enabled"
}

func (alwaysThinks) Match(r *Request) bool {
	if r == nil {
		return false
	}
	return quirk.Has(r.Provider, r.Model, quirk.NoThinkingDisable)
}

func (alwaysThinks) Apply(body []byte, r *Request) ([]byte, []string, error) {
	var notes []string
	out := body

	// 1) thinking:disabled → enabled。这个模型不能关，留着必 400。
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); has {
		if t, _ := rewrite.TopLevelString(raw, "type"); t == "disabled" {
			nb, err := rewrite.ReplaceTopLevelRaw(out, "thinking", []byte(`{"type":"enabled"}`))
			if err != nil {
				// fail-open：改不动就别改，让上游报它的错
				notes = append(notes, "thinking 未改动（"+err.Error()+"）")
			} else {
				out = nb
				notes = append(notes, `thinking:disabled → enabled（该模型不支持关闭思考）`)
			}
		}
	}

	// 2) 补显式 reasoning_effort。这是真正盖过聚合器那个隐式 disable 的一手。
	if _, has := rewrite.TopLevelRaw(out, "reasoning_effort"); !has {
		nb, err := rewrite.InsertTopLevelRaw(out, "reasoning_effort", []byte(`"low"`))
		if err != nil {
			notes = append(notes, "reasoning_effort 未补上（"+err.Error()+"）")
		} else {
			out = nb
			notes = append(notes, `补 reasoning_effort:"low"（报错原文要求 low/high/max）`)
		}
	}

	return out, notes, nil
}
