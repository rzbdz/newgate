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
"这个端口由谁绑定"的静态声明。

这条规则**不再由框架拦**：基数取消（2026-09-17）之前，多 provider 会在构图期
报 `multiple providers`，所以「别 provide 别人的端口」是自动成立的；现在多
provider 是合法的（`Get` 取第一个、`GetAll` 取全部），规则只剩约定。代价是
一次静默先到先得，所以新增扩展点一律按上一条走 `RegisterX`，别去 `Provide`
别人的端口。

owner 要实现「注册 + 撤销」这张账本时用 `component.Registry[T]`（单调 token +
按 token 精确撤销 + 写锁内跑准入检查），别自己再手写一份：

```go
type service struct {
    commands component.Registry[Command]
}

func (s *service) RegisterCommand(c Command) (component.Release, error) {
    return s.commands.Register(c, func(existing []Command) error {
        // 查重；返回非 nil 则本次注册不生效
        return nil
    })
}
```

### Component

Component 是图节点：

```go
type Component struct {
    Name     string
    Type     Type                                  // 分类标签，内核不解释取值
    Requires []Requirement
    Provides []Provision
    Start    func(context.Context, Context) error
    Attach   func(context.Context, Context) error  // 第二阶段，见下
    Stop     func(context.Context) error
}
```

一个组件可以同时是 provider 和 consumer。

### 注入边（`Inject`）与生命周期第二阶段

`Requirement` 有三个构造子，前两个建**排序边**，第三个不建：

| 构造子 | 提供者缺失 | 排序 |
| --- | --- | --- |
| `Need` | 装配失败 | 提供者在前 |
| `Optional` | 允许 | 提供者在前（有的话） |
| `Inject` | 允许 | **不参与排序** |

`Inject` 解决「互为对端」的死结：业务模块要往 ui 注入命令，而 ui 又要依赖那些
模块才能渲染——用 `Need`/`Optional` 的话两条箭头互指就是环。`Inject` 说「端口
存在就给我，我不排在它后面」，环就没有了。代价是**没有顺序保证**，见下。

因此 Manager 是**两阶段**的：

 1. 按拓扑顺序跑完全部 `Start`；
 2. 再跑一遍 `Attach` —— 此时任何端口都已提供，注入边拿得到对端。

往 ui 注入的东西（命令、状态行、体检项、诊断素材、术语）一律放 `Attach`，
并且把 `Release` 收进同一个 `releases`、在 `Stop` 里 `ReleaseAll`。

两个后果要记住：

- **被注入方（ui）的 `Stop` 必须幂等且无副作用**。注入边不排序，实测里
  `breaker` 会**在 cli 之后**才停（见 `modules/cli/module.go` 的 `New` 注释），
  晚到的 `Release` 会打到已经停掉的 ui 上。
- **`Attach` 失败会回滚整张图**（不是只回滚到失败的那个），因为那时每个组件
  都已经 `Start` 过了。

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
