# Component framework

`go/component` 是依赖和生命周期内核，不包含任何 gateway 或模型知识。

## 1. 六个概念

### Capability

Capability 是 typed port 的稳定身份：

```go
var Capability = component.NewCapability[Gateway]("gateway")
```

**端口不声明基数**（2026-09-17 取消 `One`/`Many`）。框架没法预知一个端口将来
会有几个实现，而"只能有一个"是使用者的约束、不是框架的约束——真需要唯一性
的端口（比如 `config`）由它的 owner 自己保证，不该让内核把合法装配判死。

多 provider 时 `Get` 取声明顺序里的第一个，`GetAll` 取全部。

### Requirement

Requirement 建立 provider 到 consumer 的图边：

```go
component.Need(gatewayapi.Capability)
component.Optional(cliapi.Capability)
```

缺失 `Need` 会拒绝构图；`Optional` 只放宽"没有 provider"这一种情况，端口类型
仍然严格校验。

### Provision

Provision 把 capability 和 owner 创建的值绑定：

```go
component.Provide(gatewayapi.Capability, gatewayapi.Gateway(service))
```

Consumer 只 import owner 模块的根包（端口定义在它的 `api.go`），不 import 实现。

**`Provides` 只放自己的 port**。要往别人的扩展点插东西，消费它的 service 并调
它的 `RegisterX()`——那条路带 `Release`、有查重、有生命周期，而 `Provide` 只是
"这个端口由谁绑定"的静态声明。这条规则自动成立：别人的端口已经由它的 owner
提供，再去 `Provide` 就是第二个 provider。

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

Manager 构图后把 capability table 作为 Context 交给 `Start`。`Get`/`MustGet`
按端口名取（多 provider 时取第一个），`GetAll` 取全部贡献。`MustGet` 在端口
不可用时 panic，暴露声明与使用不一致的编程错误。

### Manager

Manager 校验组件名唯一、同名端口同型、缺失 provider 和依赖环；随后拓扑启动并
逆序停止。
启动失败会对已经启动的节点做逆序 rollback。

## 2. 注册所有权

Registry capability 的注册方法返回 `component.Release`。Consumer 保存 handle，
并在 `Stop` 里调用 `ReleaseAll`。因此：

- 停止组件会移除它贡献的 hook；
- 释放顺序与注册顺序相反；
- stale release 不会删除新实例的同名注册。

## 3. 组合根

`go/app` 是唯一知道有哪些具体组件的地方——注意它在 `modules/` **之外**，
这样"`modules/` 下每个目录都是一个组件"没有例外。

装配清单由 `go/tools/genmodules` 在构建期扫描 `modules/` 生成
（`app/modules_gen.go`），所以**装一个模块 = 把目录复制进来、重新编译**，
不需要改任何清单。判据是目录含根 `module.go` 且导出 `func New() modules.Component`。

`make build` / `make test` 会自动重生成；`make check-generate` 只校验不过期
（`app` 里有一条测试跑它，拦住"加了模块忘了生成"的静默漏装配）。

`main` 显式创建 `app.New()` 并负责停止它。禁止 package init 偷偷启动全局 Manager。

## 4. Module 约定

- 每个具体模块根目录只有一个标准入口 `module.go`；
- `New()` 直接返回 `component.Component`；
- 公共 port 和跨模块值类型放在 owner 根目录的 **`api.go`**（不建 `api/` 子包，
  2026-09-17 起）：契约和它的 `NewCapability` 声明同处一文件，谁拥有哪个端口
  一眼可见；
- 不建立中心 contracts 包；
- 交叉行为使用 cross-component，如 `claudecode_deepseek`。

契约类型若实现方需要反向引用（`gateway` 的 `Request`/`Plugin` 实际由插件层
`gateway/special` 使用、`config.RoleProvider` 实际由注册表 `config/roleprov`
实现），定义就下沉到实现包、根 `api.go` 做类型别名转发。这样根包能 import
实现包而不成环。
