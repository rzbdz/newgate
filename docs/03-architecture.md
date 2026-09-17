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
| `app`（在 `modules/` 之外） | 组合根：装配清单与 `App` 所有权对象 |

`lib` 只容纳无状态、无注册、可跨组件复用的低层工具。

## 3. Owner API

Capability contract 属于提供能力的模块，写在 owner 根目录的 `api.go`（不建
`api/` 子包）。例如 Gateway hook contract 位于 `modules/gateway/api.go`，
Config role provider contract 位于 `modules/config/api.go`。Provider 和
consumer 都依赖 owner 的根包。

这样替换实现不改变 consumer，也不会产生全知的中心 contracts 包。

**跨模块贡献一律走 owner 的 `RegisterX() (Release, error)`。** 模块的 `Provides`
只放自己的 port；要往别人的扩展点插东西，就消费它的 service 再注册。注册是
运行期动作，有撤销、有查重、有生命周期；`Provide` 只是"这个端口归谁"的静态
声明。（2026-09-17 之前还有一条声明式的 `Provides: []Provision{Provide(别人的
capability, 值)}` 路径，已删除——它没有 Release、没有查重，两个模块认领同一个
命令名会静默先到先得。）owner 侧的账本用 `component.Registry[T]`，别手写。

契约类型若实现方需要反向引用，定义下沉到实现包、根 `api.go` 做类型别名转发：

| 契约 | 定义在 | 根 api.go 转发 |
| --- | --- | --- |
| Gateway `Request` / `Plugin` | `gateway/special`（插件层用它） | `type Request = special.Request` |
| Config `RoleProvider` | `config/roleprov`（注册表实现它） | `type RoleProvider = roleprov.RoleProvider` |

这样根包能 import 实现包（`gateway` → `gateway/special`），实现包不必回头
import 根包，循环依赖不会出现。

## 4. 数据与控制

磁盘配置是持久事实源。Watcher 将配置装成不可变快照，Gateway 热路径只读当前
快照。CLI 可以编辑配置、控制 daemon、查看诊断；Gateway 负责请求数据面。

Daemon 通过 pid/lock/state 文件和本地控制端点管理。升级时旧进程把 listener
移交给新进程并排空在途请求。

## 5. 当前兼容桥

部分旧数据面仍通过 package default registry 或 Runtime agent catalog 接入。
这些是明确的迁移边界，不是推荐的模块调用方式。新模块必须使用 capability 和
实例依赖，不得新增 service locator。
