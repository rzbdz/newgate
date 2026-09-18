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

// consumer 侧：拿到别人的 service 来注入
Requires: []component.Requirement{
    // 注入 ui 用 Inject 而**不是** Need/Optional —— 见下面「ui 的注入是第二阶段」
    component.Inject(cliapi.Capability),
    component.Need(confighookapi.ConfigHooksCapability),
},
Start: func(_ context.Context, ctx component.Context) error {
    // Start 只做**自己的**初始化（注册端口、装状态）
    hooks := component.MustGet(ctx, confighookapi.ConfigHooksCapability)
    release, err := hooks.RegisterStateField("mymod", "my_field")
    …
},
// 往 ui 里挂东西在 Attach 里做：那时所有端口都已提供
Attach: func(_ context.Context, ctx component.Context) error {
    ui, ok := component.Get(ctx, cliapi.Capability)
    if !ok {
        return nil // 没装 ui = 没有入口，功能照常
    }
    release, err := ui.RegisterCommand(myCommand{})
    if err != nil {
        return err
    }
    releases = append(releases, release)
    return nil
},
Stop: func(context.Context) error { return component.ReleaseAll(releases) },
```

### ui 的注入是第二阶段（`component.Inject` + `Attach`）

`modules/cli` 是**界面**，不是依赖。往它注入**不能**用 `Need`/`Optional`：那两种
都建立**排序边**，而界面自己也曾依赖那些模块（它要渲染别人报上来的 status）——
两条箭头互指就是环，环一出现，那些模块的命令就永远注入不进来，只能被迫留在界面
里。2026-09-18 之前 config / runtime / config-hook 的命令就是这样被困住的。

`component.Inject(cap)` 声明「端口存在就给我，但**我不排在它后面**」。框架分两阶段
装配：先跑完所有 `Start`，再跑所有 `Attach`。到 `Attach` 时任何一个端口都已提供，
顺序问题自然消失，界面也就**从依赖图里退出去**了——没有任何组件排在它前面或后面，
装不装 ui 只影响「这些贡献有没有地方去」。

界面上的注入点（都在 `modules/cli/extension`）：

| 端口 | 交什么 | 例子 |
| --- | --- | --- |
| `RegisterCommand` | 一条命令（`Names` 声明动词，`HelpLine` 声明它在 help 里的槽位与位置） | 每个拥有动词的模块 |
| `RegisterStatus` | `newgate status` 里的一行（`Rank` 决定行序） | gateway：代理；runtime：接管；config：配置 |
| `RegisterStatusBlocks` | status 里的成块内容（表格等，自己排版） | config：档位绑定、fallback 链 |
| `RegisterDiagnostics` | `newgate doctor` 里的一项 | config、gateway、runtime |
| `RegisterDump` | `alllogs` 诊断包里的原始素材 | config、gateway、runtime |
| `RegisterGlossary` | help 末尾术语表里属于自己的一行 | config-hook：agent；config：槽位键 |
| `RegisterVerbose` | 「我的详细模式开着」 | gateway：debug |

两条**可选接口**让命令自己声明版式例外，省得界面维护一张命令名名单：

- `Handoff` —— 这条命令马上要把控制权交给别的进程（包装启动一个客户端）；
- `Unstyled(args)` —— **这一次**的输出不是版式（JSON、KV 原文、日志流）。收 args
  是因为同一条命令可能只有某种用法不排版（`probe --json`、`profile kv`）。

**界面里不许出现的东西**：任何一条命令的实现、任何一张数据表、任何一处「读某个
模块的 state/配置文件」。`modules/cli` 的 `Requires` 是空的，`app/` 里两条棘轮测试
（`TestCLIDependenciesOnlyShrink`、`TestUIStaysOutOfTheDependencyGraph`）守着它。

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
