package api

import modules "github.com/rzbdz/newgate/go/component"

// Client 是 OpenCode 家族暴露给交叉组件的稳定身份。
// 交叉行为只需要 AgentID，不应依赖接管实现或配置文件结构。
type Client struct {
	AgentID string
}