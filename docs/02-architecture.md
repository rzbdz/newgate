# 02 · 架构

## 1. 控制面拓扑

你给的定义：`User -> FE -> BE -> cli <- User`。展开成实际形态：

```
              ┌────────────────────────────────────────────┐
   User ────▶ │ FE  Web 控制面 (浏览器)                     │
              │  provider 增删改 / 健康检查 / 默认值 / 会话  │
              └──────────────────┬─────────────────────────┘
                                 │ HTTP + SSE, 127.0.0.1, token 鉴权
              ┌──────────────────▼─────────────────────────┐
              │ BE  本地 daemon (newgate serve)             │
              │  配置写入 / 校验 / 探活 / 会话汇总 / 事件流   │
              └──────────────────┬─────────────────────────┘
                                 │ 读写（带文件锁）
              ┌──────────────────▼─────────────────────────┐
              │ Config Store   ~/.config/newgate/**         │
              │ ★ 单一事实源（唯一的真相在磁盘上）           │
              └──────────────────▲─────────────────────────┘
                                 │ 直读（daemon 不在也能工作）
              ┌──────────────────┴─────────────────────────┐
   User ────▶ │ CLI  newgate                               │
              │  解析 → 注入 → spawn → 回滚                 │
              └──────────────────┬─────────────────────────┘
                                 │ spawn(stdio: inherit)
              ┌──────────────────▼─────────────────────────┐
              │ Tool  claude / codex / opencode / hermes  │
              └──────────────────┬─────────────────────────┘
                                 │ (可选插件)
              ┌──────────────────▼─────────────────────────┐
              │ Gateway / Proxy                            │
              └──────────────────┬─────────────────────────┘
                                 ▼  Upstream LLM APIs
```

### 关键决策：CLI 不依赖 BE

**CLI 直接读磁盘上的 Config Store，不经过 daemon。**

理由：

- CLI 是热路径，用户每天敲几十次 `newgate claude`，多一次 HTTP 往返和「daemon 没起来怎么办」的分支不值得。
- 少一个必须常驻的进程 = 少一类故障模式。装完就能用。
- BE 只是 Config Store 的**编辑器 + 观测端**，不是它的看门人。

代价：并发写需要文件锁 + 原子替换（write temp → fsync → rename）。这个代价比强依赖 daemon 小。

CLI 只在两件事上**可选地**联系 BE：

1. 上报 session 生命周期（起/停），让 Web 能看到「现在谁在跑」。失败就静默跳过。
2. `injection:proxy` 时确认 gateway 在跑，不在就按策略降级或拉起。

## 2. 模块分层

```text
go/
  component/              # capability、DAG、Context、生命周期；唯一 bootstrap
  lib/                    # 无状态、无注册、跨组件复用的低层工具
  modules/
    builtin/              # 默认 Loader 与进程组件图
    gateway/              # 网关组件；forward/rewrite/special/thinkcache 是其内部包
      api/                # Gateway 自己拥有的 capability contract
    confighook/           # 配置 Hook 组件；agent/takeover/state-field 注册
    config/               # 配置语义、resolve、store、动态 role 注册
    runtime/              # daemon、launch、takeover
    cli/                  # CLI 组件；style/tui 是其内部包
    claudecode/           # 客户端组件
    deepseek/             # 模型族组件
    claudecode_deepseek/  # 交叉组件
    ...
```

编译期只要求 `component` 不 import `modules`。组件之间不靠目录层级表达依赖，
而靠 capability；`modules/builtin` 是唯一可以 import 全部具体组件的装配点。

除 `builtin`（装配器）外，每个
`go/modules/<name>/` 都是一个组件，并以根目录的 **`module.go`** 作为唯一标准
入口。`module.go` 的 `New()` 直接返回 `component.Component`，并声明
`Requires`、`Provides`、`Start` 和 `Stop`；
复杂组件可以有任意内部文件和子包，但不能把入口改叫 `component.go`、散落到子目录，
或让装配器依赖文件布局猜测入口。

能力契约不进入中心仓库。每个组件在自己的 `api/` 子包拥有公开 capability、
接口和跨组件值类型；provider 与 consumer 都只依赖该窄 API。组合组件例如
`claudecode-deepseek` 依赖 `claudecode/api`、`deepseek/api` 和 `gateway/api`，
不 import 三者实现，也不经过全知的 `modules/contracts` 中介。

### 2.1 整个运行时是一张组件图

newgate 没有“框架知道的特殊模块类型”，也没有只包一层
`Component()` 的 Provider 壳。参与运行时装配的对象都直接构造同一种
`component.Component`：

- `Name`：稳定组件名，只用于诊断；
- `Requires`：消费哪些**有类型的 capability**；
- `Provides`：提供哪些 capability 及其值；
- `Start(context.Context, Context)`：组件启动后，从 Context 取得依赖并向它注册扩展；
- `Stop(context.Context)`：按启动逆序释放资源。

注册型 capability 返回带所有权的 `component.Release`。consumer 必须保存它，
并在 `Stop` 中逆序释放；这样组件停止后不会把 hook、agent 或动态角色遗留在
provider 中，旧实例的 stale release 也不能删掉新实例注册的同名扩展。

依赖的是 capability，不是组件名。`claudecode-deepseek` 消费
`client-family.claudecode`、`model-family.deepseek` 和 `gateway`，并不 import
Claude/DeepSeek 的实现。换一个组件提供同一契约，consumer 不需要修改。

Manager 位于 `go/component`，只会校验 capability 的名字、Go 类型、单例/多例
基数，按 provider → consumer 求拓扑序，再做正序启动、逆序停止。缺 provider、
单例多 provider、类型冲突和依赖环都会拒绝启动。它不知道 request hook、agent、
state field、doctor 或 CLI command 是什么。

一个基础能力和两个扩展根能力：

1. `config` 组件拥有配置语义、解析、持久化和路径，提供 `config` 能力；Gateway、
   Runtime、CLI 和需要直接操作配置的组件显式消费它。
2. `gateway` 组件消费 `config`，提供请求扩展端口和本地入口；模型、客户端及交叉组件消费它，
   把 request/route/response hook 注册进去。
3. `config-hook` 独立提供 agent/takeover/state-field 注册端口；客户端及其配置
   扩展消费它。动态角色属于配置语义，由 `config` 自己的端口注册。

因此每个组件既可做 provider，也可做 consumer。`opencode-omo` 消费
`config-hooks` 和 `client-family.opencode`，再向配置管理器注入 takeover 与动态
角色；它的 doctor 信息和 `omo` 命令则直接提供为多例 capability，由 CLI 壳
统一渲染/调用。增加这种能力不需要修改 Manager。

具体组件平铺在 `go/modules/<name>/`；`modules/builtin` 只保存框架默认
Loader 和链接期组件清单。`main` 显式创建并停止 `builtin.App`，package init
不得偷偷启动进程级组件图。实现文件用
`var _ Interface = (*implementation)(nil)` 标明接入的标准契约，未导出函数只是
组件内部 helper。

`state.json` 允许组件拥有顶层字段，但 domain 只保存未知字段的原始 JSON，不声明
具体语义。例如 `classifier_override` 由 `claudecode` 经 `config-hooks` 注册、
由该组件解码和报告错误；`domain.State` 不知道 Bash 分类器。普通
`LoadState → SaveState` 必须无损保留这些字段。

同一扩展端口内部仍可有更细的顺序约束。例如 gateway request hooks 使用具名
`Before/After`，不使用数字 priority，也不依赖文件名或 `init()` 顺序。

v1 只启用静态 Go builtin loader。Loader 接口为后续目录 manifest、独立进程/RPC
或 WASM 预留；Go 原生 `.so plugin` 因编译器/依赖/平台强绑定且不能可靠卸载，
不作为默认动态扩展机制。Lua 不进入代理热路径，避免双语言类型、GC、sandbox
和部署复杂度先于真实需求出现。

物理目录也服从同一个模型：`go/component` 是唯一位于组件图之外的最小
bootstrap；所有有生命周期或能力所有权的代码都在 `go/modules`。纯粹、无状态、
可跨组件复用的低层工具才允许进入 `go/lib`，不能用 `utils` 名义藏业务逻辑。
仓库不再保留 `go/internal` 这棵旧分层。

`core` 不 import 任何 IO。这样「给定配置快照 + argv，应该解析出什么、生成什么注入计划」全部可以纯函数测试，不碰文件系统。

## 3. 启动时序（`newgate --provider kimi claude --resume abc`）

```
 1. argv 切分        ── 找到第一个非选项 token 'claude'
                        左边归 newgate，右边 ['--resume','abc'] 原样保留
 2. 加载配置快照      ── store.load(): 全局 + 项目级 + env 覆盖，一次性读完
 3. 解析 tool      ── 'claude' → ToolDescriptor（含 alias 查找）
 4. 解析 provider    ── 按 01-concepts §8 优先级 → Provider
 5. 协议协商        ── tool.acceptsProtocols ∩ provider.endpoints → Endpoint
 6. 解析密钥        ── SecretRef → 明文（只在内存，标记为敏感）
 7. 选注入方式       ── injections[] 里第一个 probe() 通过的
 8. 生成 plan        ── injection.plan(ctx)   ← 纯函数，--dry-run 到此为止并打印
 9. 定位可执行文件   ── which/PATH/自定义路径，找不到给出安装提示
10. 写 journal       ── 落盘「我要动哪些文件、备份在哪」，用于崩溃恢复
11. apply            ── 产生副作用；任一步失败 → 逆序回滚已完成的部分 → 退出
12. spawn            ── stdio: inherit，env = 基础 env + 注入 env
13. 转发信号        ── SIGINT/SIGTERM/SIGHUP → 子进程
14. 等待退出        ── 记录 exit code / signal
15. rollback         ── 恢复现场（幂等）
16. 清 journal       ── 删除条目
17. 退出            ── 用子进程的 exit code 退出
```

第 15 步必须在**所有**退出路径上执行：正常退出、信号、未捕获异常、`process.on('exit')`。加上第 10 步的 journal，即使被 `SIGKILL` 也能在下一次 `newgate` 启动时检测并恢复（见 [04-injection.md](04-injection.md) §5）。

## 4. 数据流：配置快照

解析阶段用**不可变快照**，避免边解析边读文件带来的竞态和不可测试性。

```ts
interface ConfigSnapshot {
  global: GlobalConfig;         // ~/.config/newgate/config.json
  providers: Provider[];
  profiles: Profile[];
  defaults: Record<string, string>;   // toolId -> profileId | providerId
  tools: ToolDescriptor[];        // 内置 ⊕ 用户覆盖
  project?: ProjectConfig;            // 向上查找到的 .newgate.json
  env: Record<string, string>;        // 进程 env 里 NEWGATE_* 的部分
  loadedAt: string;
}
```

`resolve(snapshot, argv) → ResolvedConfig | ResolveError`：纯函数。

```ts
interface ResolvedConfig {
  tool: ToolDescriptor;
  provider: Provider;
  endpoint: Endpoint;
  models: ModelMap;
  credential: Secret;            // 带 redaction 的包装类型，toString() 输出 '***'
  extraEnv: Record<string, string>;
  extraArgs: string[];
  injection: InjectionKind;
  passthroughArgs: string[];
  provenance: Provenance;        // 每个字段来自哪一层，`newgate status` 和报错信息要用
}
```

`provenance` 很重要：用户搞不清「为什么用了这个 provider」时，`newgate status --explain` 要能回答「因为当前目录 `.newgate.json` 第 3 行」。

## 5. 扩展点

| 扩展点 | 形式 | 用途 |
| --- | --- | --- |
| Tool | JSON/TOML 描述符放进 `~/.config/newgate/tools/` | 接新的 CLI，无需改代码 |
| Injection | 实现 `Injection` 接口并注册 | 特殊的注入方式（如写 keychain、改 shell rc） |
| Protocol | 注册协议名 + 网关翻译器 | 新的 API 方言 |
| SecretResolver | `scheme:` 前缀注册 | 接 1Password / vault / 自研 KMS |
| Plugin | gateway 就是第一个插件 | 独立进程，通过 Config Store + 本地 socket 集成 |

优先保证 **Tool 是纯数据**：接一个新 CLI 的成本应该是「写 30 行 JSON」，而不是「提 PR 改 switch-case」。这是这个项目能不能长期活下去的关键。

## 6. 测试策略

| 层 | 怎么测 |
| --- | --- |
| `core` | 纯函数快照测试：给定 snapshot + argv，断言 ResolvedConfig 与 provenance |
| argv 切分 | 表驱动用例，覆盖 [03-cli-spec.md](03-cli-spec.md) 里的每一条透传规则 |
| `store` | 临时目录 + 并发写压测（验证锁与原子替换） |
| `runtime` | 用一个假 tool（打印 env 和 argv 到 stdout 的脚本）做端到端断言 |
| 崩溃恢复 | 真的 `SIGKILL` 掉进程，再跑一次 newgate，断言配置文件被还原 |
| tools | 契约测试：每个描述符跑一遍 schema 校验 + plan 生成快照 |
