package special

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/internal/gateway/rewrite"
)

func init() { Register(glm{}) }

// glm 修 GLM 系模型「没写 thinking 就默认思考」的语义坑。
//
// 现场同 st-claude-bg：glm-5.3 对不带 thinking 字段的请求照样思考（实抓：
// 裸请求响应里 reasoning_content 非空）。Anthropic 协议里「没写」的语义是
// 不思考，Claude 家族也是这么实现的；GLM 把缺省当成了开。于是「客户端
// 没要求思考」的请求被拖进 15-30 秒的思考。
//
// 修法与 st-deepseek 第 1 手同款：**只在客户端没写 thinking 时**补显式
// disabled；写了（enabled/adaptive）就一个字节不动——那是客户端真要思考。
// 与 claude-bg 的分工：那边按客户端（claude 的后台非流式请求，带了也改写），
// 这边按模型（glm 系，任何客户端，只补缺）。
//
// 摘除条件：GLM 把缺省改成不思考（或网关替它改了），
// `newgate st off glm` 即可验证；确认不需要了整文件可删。
type glm struct{}

func (glm) Name() string { return "glm" }

func (glm) Why() string {
	return "GLM 系模型把「没写 thinking」当默认开思考（Anthropic 语义是关）" +
		"→ 没要求思考的请求被拖进十几秒\n" +
		"客户端没写就补显式 disabled；写了就不动"
}

// Match 只认 GLM：模型名、provider 名、base URL 任一处出现 glm
// （聚合网关可能改模型名，provider 名或 endpoint 里通常仍留着痕迹）。
// Anthropic 官方端点一律不碰（同 st-deepseek 的反向兜底）。
func (glm) Match(r *Request) bool {
	if r == nil {
		return false
	}
	if strings.Contains(strings.ToLower(r.BaseURL), "api.anthropic.com") {
		return false
	}
	for _, s := range []string{r.Model, r.Provider, r.BaseURL} {
		if strings.Contains(strings.ToLower(s), "glm") {
			return true
		}
	}
	return false
}

func (glm) Apply(body []byte, r *Request) ([]byte, []string, error) {
	if _, has := rewrite.TopLevelRaw(body, "thinking"); has {
		return body, nil, nil // 客户端写了：尊重，一个字节不动
	}
	if _, effort := rewrite.TopLevelRaw(body, "reasoning_effort"); effort {
		return body, nil, nil // 设了推理强度 = 明确要思考
	}
	if _, has := rewrite.TopLevelRaw(body, "messages"); !has {
		return body, nil, nil // /v1/models 之类，没东西可补
	}
	nb, err := rewrite.InsertTopLevelRaw(body, "thinking",
		[]byte(`{"type":"disabled"}`))
	if err != nil {
		return nil, nil, fmt.Errorf("注入 thinking 失败: %w", err)
	}
	return nb, []string{`注入 thinking:{"type":"disabled"}（GLM 把缺省当默认思考）`}, nil
}
