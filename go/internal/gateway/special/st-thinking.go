package special

import (
	"github.com/rzbdz/newgate/go/internal/gateway/quirk"
	"github.com/rzbdz/newgate/go/internal/gateway/rewrite"
)

func init() { Register(alwaysThinks{}) }

// BestEffortDisableThink 「这次调用不想思考」的完整语义，一个操作做完：
//
//	1. 落地意图：**只在没写 thinking 时**注入 thinking:{"type":"disabled"}。
//	   写了（adaptive / enabled / disabled）就是客户端这次调用的明确意图，
//	   尊重它、不碰它（2026-09-09 实抓 + 探针复现：compact 总结请求显式带
//	   thinking:adaptive，强改成 disabled 会被「始终思考」模型拒掉——glm-5.3
//	   code 1210；探针：adaptive+tools→200、disabled→1210）；
//	2. 按当前链步的模型翻译：不支持关闭思考的模型（quirk NoThinkingDisable）
//	   听不懂 disabled，就地翻成 enabled + reasoning_effort:low。
//
// best effort 的含义：关不掉就让它思考（用户把不支持关思考的模型配进
// light 档，也只能由他去了——慢，但请求活着）；翻译不动就原样发。绝不
// 因为这个意图让请求失败。
//
// 架构位置：意图方（如 claude-bg 的后台调用）在 special 链里调用它；
// forward 的链循环保证它对**每一步的模型**各跑一遍（fallback 换了模型，
// 翻译跟着换）。always-thinks 插件是它的兜底半边——管别的来源写入的
// disabled 和「带 tools 没写 thinking」的 1210 形态，两边共用同一个翻译
// 核心（noDisableTranslate），语义只有一份。
func BestEffortDisableThink(body []byte, r *Request) ([]byte, []string, error) {
	const why = "这次调用不想思考（best effort）"
	var notes []string
	out := body

	// reasoning_effort 与 thinking:disabled 互斥（见 st-deepseek 同款注释）：
	// 客户端设了推理强度就别去关它。
	if _, effort := rewrite.TopLevelRaw(out, "reasoning_effort"); effort {
		return out, nil, nil
	}

	// 1) 落地意图——只在「没写 thinking」时注入 disabled。
	//    写了（adaptive/enabled/disabled）就是客户端这次调用的明确意图，尊重
	//    它、不动它：compact（总结）请求显式带 thinking:adaptive，强改成
	//    disabled 会被「始终思考」模型拒掉（glm-5.3 code 1210）。best effort
	//    的边界从「关不掉就让它想」收窄成「本来就没说要想，才替它说不想」。
	if _, has := rewrite.TopLevelRaw(out, "thinking"); !has {
		nb, err := rewrite.InsertTopLevelRaw(out, "thinking", []byte(`{"type":"disabled"}`))
		if err != nil {
			notes = append(notes, "thinking 未注入（"+err.Error()+"）")
		} else {
			out = nb
			notes = append(notes, `注入 thinking:{"type":"disabled"}（`+why+`）`)
		}
	}

	// 2) 模型听不懂 disabled？就地翻译成它听得懂的最小思考。
	//    翻译按 (provider, r.Model) 的 quirk 决定——body 已被切到别的模型
	//    就别动手：「glm-5.3 不能关思考」推不出「glm-4.5-air 不能关」。
	if bm, has := rewrite.TopLevelString(out, "model"); (!has || bm == "" || bm == r.Model) &&
		quirk.Has(r.Provider, r.Model, quirk.NoThinkingDisable) {
		if nb, translated, ns, err := noDisableTranslate(out); err == nil && translated {
			out = nb
			notes = append(notes, ns...)
		}
	}
	return out, notes, nil
}

// noDisableTranslate 翻译核心（只有这一份语义）：把 thinking:disabled 翻成
// 始终思考模型听得懂的 enabled + reasoning_effort:low。只在 thinking 确实
// 是 disabled 时动手；translated=false 表示没什么可翻的。
//
// 现场报错（智谱 GLM，code 1210）：
//
//	[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。][2026…]
//
// 实测复现（api.rvcompute.com 聚合器 + glm-5.3）：
//
//	tools + 不给 reasoning_effort       → 400 / 1210
//	tools + reasoning_effort=low|high   → 200
//	thinking:{"type":"disabled"}        → 400 / 1210
//	不带 tools、也不带 thinking          → 200
//
// 成因：聚合器看见 tools 就替我们给上游塞了「关闭思考」，而始终思考的模型
// 直接拒。改不了聚合器，但显式给一个思考强度就能盖过它塞的值；low 最接近
// 「别想太多」的本意（实测 none 也收，但报错原文没提它，不赌）。
func noDisableTranslate(body []byte) (out []byte, translated bool, notes []string, err error) {
	raw, has := rewrite.TopLevelRaw(body, "thinking")
	if !has {
		return body, false, nil, nil
	}
	if t, _ := rewrite.TopLevelString(raw, "type"); t != "disabled" {
		return body, false, nil, nil
	}
	nb, rerr := rewrite.ReplaceTopLevelRaw(body, "thinking", []byte(`{"type":"enabled"}`))
	if rerr != nil {
		return body, false, nil, rerr
	}
	notes = append(notes, `thinking:disabled → enabled（该模型不支持关闭思考）`)

	// effort 跟着补上（已有了就不动——客户端设过强度就尊重）
	if _, has := rewrite.TopLevelRaw(nb, "reasoning_effort"); !has {
		if nb2, ierr := rewrite.InsertTopLevelRaw(nb, "reasoning_effort", []byte(`"low"`)); ierr == nil {
			nb = nb2
			notes = append(notes, `补 reasoning_effort:"low"（报错原文要求 low/high/max）`)
		}
		// effort 补不上：thinking 至少翻过去了，fail-open 继续
	}
	return nb, true, notes, nil
}

// alwaysThinks 兜底翻译器：「始终思考」模型收到「关闭思考」就 400
// （GLM 1210）。BestEffortDisableThink 是意图方的主动入口；这个插件管
// **别的来源**造成的 400 形态：
//
//   - 客户端自己的 settings 写了 thinking:disabled（泄漏进后台调用，或
//     用户真想关——分不出来，一律翻）；
//   - 带 tools 且没写 thinking：聚合器会隐式替我们关思考 → 1210。
//
// 为什么 Match 只信 quirk 注册表、不按模型名猜：「始终思考」是模型版本的
// 属性，会变，也没接口能查。按名字猜会给一堆无关请求乱加字段，比不修更糟。
// 注册表的两个来源：转发时撞上这个报错学到（一次失败换永久免疫），或者
// newgate probe 主动探出来。
type alwaysThinks struct{}

func (alwaysThinks) Name() string { return "always-thinks" }

func (alwaysThinks) Why() string {
	return "有些模型始终思考，收到「关闭思考」就 400（GLM code 1210）\n" +
		"给这些模型补显式 reasoning_effort=low，并把 thinking:disabled 改回 enabled" +
		"（BestEffortDisableThink 的兜底半边）"
}

func (alwaysThinks) Match(r *Request) bool {
	if r == nil {
		return false
	}
	return quirk.Has(r.Provider, r.Model, quirk.NoThinkingDisable)
}

func (alwaysThinks) Apply(body []byte, r *Request) ([]byte, []string, error) {
	// 不变量保险：quirk 是按 (provider, r.Model) 学的，只对还发往这个模型
	// 的请求生效。链上 r.Model 由框架从 body 同步（special.Apply），正常
	// 到不了这里就不相等；直调或将来有新路径时这一道把「补丁打错模型」
	// 挡在最后关口。
	if bm, has := rewrite.TopLevelString(body, "model"); has && bm != r.Model {
		return body, nil, nil
	}

	var notes []string
	out := body
	needEffort := false

	if _, hasThinking := rewrite.TopLevelRaw(out, "thinking"); hasThinking {
		// disabled → 翻译（与 BestEffortDisableThink 共用同一份核心）
		if nb, translated, ns, err := noDisableTranslate(out); err != nil {
			notes = append(notes, "thinking 未改动（"+err.Error()+"）")
		} else if translated {
			out = nb
			notes = append(notes, ns...)
		}
	} else if _, hasTools := rewrite.TopLevelRaw(out, "tools"); hasTools {
		// 没写 thinking 还带 tools：聚合器会隐式替我们关思考 → 1210。
		// （显式写了 adaptive/enabled 的不在此列——客户端要思考，没人隐式关它。）
		needEffort = true
	}

	// 补显式 reasoning_effort（tools 形态）。这是真正盖过聚合器那个隐式
	// disable 的一手。
	if needEffort {
		if _, has := rewrite.TopLevelRaw(out, "reasoning_effort"); !has {
			if nb, err := rewrite.InsertTopLevelRaw(out, "reasoning_effort", []byte(`"low"`)); err != nil {
				notes = append(notes, "reasoning_effort 未补上（"+err.Error()+"）")
			} else {
				out = nb
				notes = append(notes, `补 reasoning_effort:"low"（报错原文要求 low/high/max）`)
			}
		}
	}

	return out, notes, nil
}
