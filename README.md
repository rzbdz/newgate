# newgate

newgate 是 AI CLI 的本地语义模型网关。Claude Code 和 OpenCode 只需要表达
“这次任务需要哪个能力档”，真实 provider、模型、fallback 和上游兼容处理由
newgate 统一决定。

```text
AI CLI -> heavy / normal / mid / light -> profile chain -> provider/model
```

当前能力档：

| Role | 含义 |
| --- | --- |
| `heavy` | 最强能力，适合困难推理 |
| `normal` | 日常主力档 |
| `mid` | 成本和能力折中 |
| `light` | 快速、低成本任务 |
| `vision` | 正交的视觉能力 |

Profile 把 role 绑定到候选链。切换 profile 后，新请求立即使用新链，不需要修改
每个客户端的模型配置。

## 当前实现

- 本地 HTTP gateway，支持 Anthropic 和 OpenAI 请求方言；
- provider/model 路由、稀疏 profile、fallback 和首字节超时；
- Claude Code PATH shim 和 OpenCode 配置接管；
- DeepSeek reasoning content 保存、回填和跨 provider tool-loop 迁移；
- 请求级 special treatment 插件，改写必有日志，失败默认 fail-open；
- 热更新配置、探活、metrics、请求取证和零停机 daemon 交接；
- 单文件静态 Go 二进制。

## 快速开始

```bash
cd go
make build
bin/newgate init
bin/newgate start
bin/newgate status
```

常用诊断：

```bash
newgate tier normal
newgate probe
newgate metrics
newgate st
newgate doctor
newgate logs
```

## 代码结构

```text
go/component       typed capability graph and lifecycle kernel
go/lib             stateless shared helpers
go/modules/config  configuration semantics and persistence
go/modules/gateway routing data plane and extension execution
go/modules/runtime daemon, launch, injection, and takeover
go/modules/cli     local control plane
go/modules/*       client, model, and cross-components
```

每个模块根目录以 `module.go` 为标准入口，公开契约属于模块自己的 `api/`。
从 [docs/00-index.md](docs/00-index.md) 开始阅读当前实现。

## 构建与验证

```bash
cd go
go test ./...
go vet ./...
go test -race ./...
make static
cd ..
bash mock/e2e.sh
bash mock/e2e_claude.sh
```

测试默认不访问真实上游，不消耗 token。
