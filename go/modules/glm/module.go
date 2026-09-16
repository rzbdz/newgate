// Package glm 拥有 GLM 模型家族的身份判断。
//
// 它目前不需要单方协议修补，所以组件只提供 Model capability。需要同时了解
// Claude Code 和 GLM 的行为由 claudecode_glm 组合，避免为了未来可能性预建
// 空插件或把客户端知识塞进模型模块。
package glm

import (
	modules "github.com/rzbdz/newgate/go/component"
	glmapi "github.com/rzbdz/newgate/go/modules/glm/api"
)

