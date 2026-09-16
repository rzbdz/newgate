# 当前架构

## 1. 运行图

```text
Config
  ├─ Gateway
  ├─ Runtime
  └─ CLI

ConfigHook
  ├─ Claude Code
  ├─ OpenCode
  └─ OpenCode OMO

Gateway
  ├─ Thinking
  ├─ Claude Code behavior
  ├─ DeepSeek behavior
  └─ cross-components
```

箭头表示 capability 消费，不表示目录 import。实际启动顺序由 component Manager
根据 `Requires`/`Provides` 计算。

## 2. 目录所有权

| 目录 | 所有权 |
| --- | --- |
| `component` | capability graph 和生命周期 |
| `modules/config` | domain、profile resolver、store、动态 role |
| `modules/confighook` | Agent、takeover、state field registry |
| `modules/gateway` | HTTP 数据面和扩展执行 |
| `modules/runtime` | daemon、进程启动、shim、takeover |
| `modules/cli` | 命令解析、TUI、诊断输出 |
| `modules/<client>` | 客户端描述和客户端独有行为 |
| `modules/<model>` | 模型族识别和上游独有行为 |
| `modules/<client>_<model>` | 只存在于交叉点的行为 |

`lib` 只容纳无状态、无注册、可跨组件复用的低层工具。

## 3. Owner API

Capability contract 属于提供能力的模块。例如 Gateway hook contract 位于
`gateway/api`，Config role provider contract 位于 `config/api`。Provider 和
consumer 都依赖 owner API。

这样替换实现不改变 consumer，也不会产生全知的中心 contracts 包。

## 4. 数据与控制

磁盘配置是持久事实源。Watcher 将配置装成不可变快照，Gateway 热路径只读当前
快照。CLI 可以编辑配置、控制 daemon、查看诊断；Gateway 负责请求数据面。

Daemon 通过 pid/lock/state 文件和本地控制端点管理。升级时旧进程把 listener
移交给新进程并排空在途请求。

## 5. 当前兼容桥

部分旧数据面仍通过 package default registry 或 Runtime agent catalog 接入。
这些是明确的迁移边界，不是推荐的模块调用方式。新模块必须使用 capability 和
实例依赖，不得新增 service locator。
