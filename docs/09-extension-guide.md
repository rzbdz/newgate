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
