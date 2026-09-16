# Component framework

`go/component` 是依赖和生命周期内核，不包含任何 gateway 或模型知识。

## 1. 六个概念

### Capability

Capability 是 typed port 的稳定身份：

```go
var Capability = component.One[Gateway]("gateway")
```

`One[T]` 只允许一个 provider；`Many[T]` 聚合多个扩展贡献。

### Requirement

Requirement 建立 provider 到 consumer 的图边：

```go
component.Need(gatewayapi.Capability)
component.Optional(cliapi.CommandsCapability)
```

缺失 `Need` 会拒绝构图；`Optional` 可以为空。

### Provision

Provision 把 capability 和 owner 创建的值绑定：

```go
component.Provide(gatewayapi.Capability, gatewayapi.Gateway(service))
```

Consumer 只 import owner 的 `api`，不 import 实现。

### Component

Component 是图节点：

```go
type Component struct {
    Name     string
    Requires []Requirement
    Provides []Provision
    Start    func(context.Context, Context) error
    Stop     func(context.Context) error
}
```

一个组件可以同时是 provider 和 consumer。

### Context

Manager 构图后把 capability table 作为 Context 交给 `Start`。`MustGet`
取得 `One`，`GetAll` 取得 `Many`。错误基数会立即 panic，避免静默取错服务。

### Manager

Manager 校验名称、类型、基数、缺失 provider 和依赖环；随后拓扑启动并逆序停止。
启动失败会对已经启动的节点做逆序 rollback。

## 2. 注册所有权

Registry capability 的注册方法返回 `component.Release`。Consumer 保存 handle，
并在 `Stop` 里调用 `ReleaseAll`。因此：

- 停止组件会移除它贡献的 hook；
- 释放顺序与注册顺序相反；
- stale release 不会删除新实例的同名注册。

## 3. 组合根

`modules/builtin` 是唯一知道所有具体组件的地方。`main` 显式创建 `builtin.App`
并负责停止它。禁止 package init 偷偷启动全局 Manager。

## 4. Module 约定

- 每个具体模块根目录只有一个标准入口 `module.go`；
- `New()` 直接返回 `component.Component`；
- 公共 port 和跨模块值类型放在 owner 的 `api/`；
- 不建立中心 contracts 包；
- 交叉行为使用 cross-component，如 `claudecode_deepseek`。
