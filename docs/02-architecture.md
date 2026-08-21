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

```
packages/
  core/            # 无 IO 的领域层：类型、解析、plan 生成、schema 校验
    concepts/      #   Provider / Endpoint / Tool / Profile / Session 类型
    resolve/       #   解析优先级流水线（纯函数，输入快照 → 输出 ResolvedConfig）
    injection/     #   Injection 接口 + plan() 纯逻辑
    protocol/      #   协议协商、模型槽位映射

  store/           # Config Store：读写、文件锁、原子替换、迁移、密钥引用解析
  tools/         # 内置 tool 描述符（数据为主）+ 少量特例适配代码
  runtime/         # 有 IO 的执行层：apply/rollback、spawn、信号转发、journal、崩溃恢复
  cli/             # 参数解析、子命令、输出渲染
  server/          # BE：HTTP API、SSE、探活、session 汇总
  web/             # FE
  gateway/         # 插件：反向代理、协议翻译、路由策略、用量统计
```

依赖方向严格单向：

```
cli ──▶ runtime ──▶ core
 │         │         ▲
 │         └──▶ store┘
 └──▶ tools ──▶ core
server ──▶ store, core, (runtime 只读部分)
gateway ──▶ core (只用 protocol/ 和类型)
web ──▶ server (HTTP)
```

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
