# 扩展开发

## 1. 新组件

在 `go/modules/<name>/module.go` 定义：

```go
func New() component.Component
```

按需声明 `Requires`、`Provides`、`Start` 和 `Stop`。**不需要改任何清单**：
装配清单由 `go/tools/genmodules` 构建期扫描 `modules/` 生成，目录进来就自动
在图上（判据：根 `module.go` 导出 `func New() modules.Component`）。
`make build` 会自动重生成，`make check-generate` 只校验。

## 2. 新 capability

Contract 放在 owner 根目录的 `api.go`：

```go
type Service interface {
    Do(context.Context) error
}

var Capability = component.NewCapability[Service]("owner.service")
```

接口只包含 consumer 真正需要调用的行为。空 marker 不是 service；不要为了排序
声明一个从不读取的 Requirement。

端口不声明基数：需要"只能有一个"就在 owner 的注册逻辑里保证，框架不拦多
provider。

## 3. 跨模块贡献：一律走 register

**`Provides` 只放自己的 service。** 要往别人的扩展点插东西，消费它的 service
并调它的 `RegisterX()`：

```go
// owner 侧：service 上开注册方法
type Service interface {
    RegisterPlugin(Plugin) (component.Release, error)
}

// consumer 侧：Consume 别人的 service 来注入
Requires: []component.Requirement{
    component.Need(cliapi.Capability),
},
Start: func(_ context.Context, ctx component.Context) error {
    cli := component.MustGet(ctx, cliapi.Capability)
    release, err := cli.RegisterCommand(myCommand{})
    if err != nil {
        return err
    }
    releases = append(releases, release)
    return nil
},
Stop: func(context.Context) error { return component.ReleaseAll(releases) },
```

注册 API 返回 `(component.Release, error)`，在写锁内做查重、冲突返回错误（不要
静默先到先得）；consumer 在 `Stop` 中逆序释放。

**owner 侧要写这张账本就用 `component.Registry[T]`**，别自己再手写一份：它是
单调 token + 按 token 精确撤销 + 可选的写锁内准入检查。陈旧 `Release` 是静默
no-op（`Stop` 幂等），零值可用（`service` 里直接内嵌一个字段即可）。

```go
type service struct {
    commands component.Registry[Command]   // 零值可用，New() 里不用构造
}

func (s *service) RegisterCommand(c Command) (component.Release, error) {
    return s.commands.Register(c, func(existing []Command) error {
        for _, other := range existing {
            // 名字撞车就返回错误——这条是给插件作者看的业务冲突，
            // 报错文案用中文（跟模块里其他用户可见文案一致）。
        }
        return nil
    })
}
```

gateway/special、config/roleprov、confighook 里那三份手写账本**暂时不迁**：
它们各有真实差异（插件拓扑排序、`Refresh` 读侧、三张异质表共享一张 token），
强行归并会造出更差的抽象。`Registry` 先在 `cli` 上验证，再逐个迁。

## 4. 新 Agent

Agent 组件通常：

1. 消费 ConfigHook；
2. 注册 Agent descriptor；
3. 可选绑定配置 takeover；
4. 可选提供 client-family capability；
5. Stop 时释放注册。

## 5. 新模型族

模型组件负责识别目标 provider/model，并注册只属于该模型族的 Gateway 行为。
匹配应基于明确的 provider、model 或 base URL 证据。

如果这个模型族的上游会用自己的方言报一类「**请求形状**错误」——同一份 body 换
哪个 provider 都会以同样方式被拒，因此不该记在任何一家的可用性账本上——就把它
写成一条**形状判据**注册进健康表（`modules/breaker`），别写进转发热路径：

```go
type ShapeDetector interface {
    Name() string                     // 进日志/指标/证据目录名，是一条对外契约
    Match(status int, body []byte) bool
}
```

```go
// modules/<模型族>/module.go
Requires: []component.Requirement{
    component.Need(breakerapi.Capability),
},
Start: func(_ context.Context, ctx component.Context) error {
    health := component.MustGet(ctx, breakerapi.Capability)
    release, err := health.RegisterShapeDetector(reasoningShape{})
    if err != nil {
        return err
    }
    releases = append(releases, release)
    return nil
},
Stop: func(context.Context) error { return component.ReleaseAll(releases) },
```

契约要点（DeepSeek 的 `modules/deepseek/shape.go` 是参考实现，它的表测试就是
这条判据的行权点）：

- **core 里不许出现上游专有字符串**。转发路径只读 `Result.Shape` 那个名字
  （判据自己起的），据此打 `[shape-400]` 日志、存
  `dump/shape-400-<名字>/`；它不知道也不用知道那两句文案长什么样。
- **判据要宽进严出**：只认真正属于这一类的那几发。一条见 400 就认领的判据会
  把「上游真的在拒我们的请求」一起放过——那正是它存在要区分的东西。
- **命中之后永不摘牌，只计数**（`shape_skips` + metrics
  `breaker.skipped.shape_error`），这条策略在 `modules/breaker` 里，判据自己
  改不了。
- **`Name()` 别乱改**：它进日志、指标和证据目录名，改名字等于让老证据换地方。
  非字母数字的字符在落盘时会被换成 `_`，空名退化成 `unknown`。
- 判据**不允许 panic 穿出去**（健康表会 recover 并当作不认领），但也别依赖这层
  兜底：判据在每一个上游 4xx 上都会跑，写成纯函数、只做字符串匹配。

## 6. 新上游补丁

放在 owner 模块的 `st-*.go`，实现 Gateway Plugin：

- `Name` 稳定且唯一；
- `Why` 写真实上游错误和存在理由；
- `Match` 缩小到正确范围；
- `Apply` 只做必要 byte splice；
- 返回 notes；
- 出错 fail-open。

只存在于“客户端 × 模型”的逻辑放到 cross-component，不能污染任一单边模块。

## 7. 文档和历史

新增概念先在所属 package comment 中说明问题、边界和依赖方向；公开契约旁的
注释解释调用者必须遵守的语义。只有跨 package 行为变化时才更新对应专题文档，
不要为每个声明机械复制一份历史说明。Build tag 是完整、可编译的模块里程碑。
