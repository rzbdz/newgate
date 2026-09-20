# 文档索引

这里的文档只描述当前实现，不保存旧路线图、竞品调研或未实现设计。

| 顺序 | 文档 | 回答的问题 |
| --- | --- | --- |
| 1 | [01-product.md](01-product.md) | newgate 解决什么问题？ |
| 2 | [02-component-framework.md](02-component-framework.md) | 组件内核有哪些概念？ |
| 3 | [03-architecture.md](03-architecture.md) | 当前模块如何连接？ |
| 4 | [04-configuration.md](04-configuration.md) | 配置和 fallback 如何解析？ |
| 5 | [05-gateway.md](05-gateway.md) | 请求如何经过 gateway？ |
| 6 | [06-reasoning.md](06-reasoning.md) | reasoning content 为什么会丢，如何恢复？ |
| 7 | [07-clients-runtime.md](07-clients-runtime.md) | Claude/OpenCode 如何被接管？ |
| 8 | [08-operations.md](08-operations.md) | 如何运行、升级和诊断？ |
| 9 | [09-extension-guide.md](09-extension-guide.md) | 如何新增组件或上游补丁？ |
| 10 | [10-testing-security.md](10-testing-security.md) | 如何验证并守住安全边界？ |
| 11 | [11-troubleshooting.md](11-troubleshooting.md) | 常见故障如何定位？ |
| 12 | [12-newgate-remote.md](12-newgate-remote.md) | newgate-remote：基于 Tailscale 的分布式网关设计（仅设计文档，未实现） |
| 13 | [13-i18n.md](13-i18n.md) | 界面与日志怎么本地化？为什么键就是那句英文？翻译流程怎么跑？ |

## 历史阅读

`build-01-*` 到 `build-17-*` 是可编译里程碑。tag 之间的提交按依赖顺序逐步
引入代码概念；中间提交可能暂时不能编译，对应 build tag 必须通过
`go test ./...`。设计动机优先写在概念所属 package 和声明旁，专题文档只描述
跨 package 的完整行为。

## 当前术语

| 术语 | 定义 |
| --- | --- |
| Provider | 一个上游入口、协议和凭证 |
| Model | provider 暴露的真实模型名 |
| Role | `heavy`、`normal`、`mid`、`light`；`vision` 正交 |
| Binding | `provider/model` |
| Profile | role 到候选 binding 链的配置 |
| Agent | 被接管的 AI CLI，如 Claude Code、OpenCode |
| Component | capability graph 中拥有生命周期的节点 |
| Capability | 组件之间使用的 typed port |
