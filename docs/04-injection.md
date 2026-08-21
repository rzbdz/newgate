# 04 · 注入（Injection）

> **⚠ 定位已变更，读之前先看 [16-core-thesis.md](16-core-thesis.md)。**
>
> 本文档最初假设「每次运行都改配置/注入 env」。在确定 **full proxy 为核心**之后，注入降级为 **bootstrap（一次性引导）**：把 tool 的配置指向代理，装一次，此后只读。
>
> 因此下文中这些部分**在 L0/L1 主路径上不再需要**：§3.2 的 inplace 冲突处理、§5 的 journal 崩溃恢复、退出时 rollback、sandbox 隔离。它们只在 **L2/L3 兜底路径**（每次启动都要包装注入）下仍然适用，故保留。
>
> `plan/apply/rollback` 接口本身不变——`rollback` 的语义从「每次退出时还原」变成「`newgate off` 时卸载」。

注入是 newgate 的引导机制：把解析出来的 provider 配置，变成目标 CLI 真的能读到的东西，并且**卸载之后现场干净**。

## 1. 统一接口

```ts
type InjectionKind = 'env' | 'config' | 'composite' | 'proxy';

interface Injection {
  kind: InjectionKind;

  /** 当前环境下这种注入可用吗？不可用时返回原因，供降级和 doctor 使用 */
  probe(ctx: ResolveContext): Probe;          // { ok: true } | { ok: false, reason: string }

  /** 纯函数：算出「要做什么」，不产生任何副作用。--dry-run 只走到这里 */
  plan(ctx: ResolveContext): InjectionPlan;

  /** 执行副作用，返回回滚句柄 */
  apply(plan: InjectionPlan): Applied;

  /** 幂等：重复调用、部分失败后调用都必须安全 */
  rollback(applied: Applied): void;
}

interface InjectionPlan {
  kind: InjectionKind;
  env: Record<string, string>;        // 要注入子进程的环境变量
  fileOps: FileOp[];                  // 要做的文件操作（config 注入才有）
  args: string[];                     // 要追加给 tool 的参数
  describe(): string;                 // 人类可读，--dry-run 打印这个
  children?: InjectionPlan[];         // composite
}

interface FileOp {
  op: 'patch' | 'write' | 'clone-dir';
  path: string;
  format: 'json' | 'jsonc' | 'toml' | 'yaml' | 'ini' | 'dotenv';
  patch?: PatchSpec[];                // { pointer: '/env/ANTHROPIC_BASE_URL', value: '...' }
  backupStrategy: 'inplace' | 'sandbox';
}
```

**`plan()` 必须是纯函数**，这样 `--dry-run` 的输出就是真实将要发生的事，而不是另写一套「大概会这样」的描述。

## 2. injection:env

最简单、最安全、**默认首选**。只构造子进程的 environment，父进程和磁盘都不动。

```
plan.env = {
  ANTHROPIC_BASE_URL: endpoint.baseUrl,
  ANTHROPIC_AUTH_TOKEN: <secret>,
  ANTHROPIC_MODEL: models.primary,
  ...provider.headers 映射,
  ...profile.env,
  NEWGATE_SESSION_ID: session.id,
}
```

具体变量名由 tool 描述符的 `envMap` 决定（见 [05-tools.md](05-tools.md)），注入代码本身不认识 `ANTHROPIC_*`。

### 需要注意

- **清理干扰变量**：用户 shell 里可能已经 `export ANTHROPIC_API_KEY=...`，与我们注入的 `ANTHROPIC_AUTH_TOKEN` 打架。描述符要能声明 `unsetEnv: ['ANTHROPIC_API_KEY']`，从子进程 env 中删除。这是最常见的「为什么切了 provider 还是走老的」故障源。
- 空值语义：值为 `null` = 删除该变量，值为 `''` = 设为空串。两者要区分。
- 环境变量对某些 tool 的**优先级可能低于其配置文件**。这时 env 注入会静默失效——必须在 tool 描述符里标注 `envBeatsConfig: false`，此时 newgate 应拒绝单用 env 注入，改用 composite。

## 3. injection:config

Tool 只认配置文件、或配置文件优先级更高时使用。有两种模式，**优先 sandbox**。

### 3.1 sandbox 模式（推荐）

不动用户的原配置。把配置目录复制到临时目录，改副本，通过环境变量让 tool 去读副本。

```
1. mkdtemp  →  $TMPDIR/newgate-<sid>/
2. 复制 ~/.claude/  →  $TMPDIR/newgate-<sid>/claude/   （可按描述符只复制必要文件）
3. patch 副本里的 settings.json
4. env: CLAUDE_CONFIG_DIR=$TMPDIR/newgate-<sid>/claude
5. 退出后 rm -rf 整个临时目录
```

优点：

- **零风险**：原文件一个字节都不改，崩溃了也不需要恢复。
- **并发安全**：同时开五个 `newgate claude`，各用各的 provider，互不干扰。这是 inplace 模式做不到的。

前提：tool 支持「配置目录/文件路径」环境变量。描述符字段 `configDirEnv`。不支持的 tool 只能走 inplace。

**待确认**：各 tool 是否真的都提供了这个 env（Claude Code 的 `CLAUDE_CONFIG_DIR`、Codex 的 `CODEX_HOME` 等），需要对着上游文档逐个核实，见 [05-tools.md](05-tools.md) 的核实清单。

### 3.2 inplace 模式（兜底）

```
1. 取文件锁（flock）——同一个配置文件同时只允许一个 newgate session
2. 读原文件，算 sha256
3. 备份到 ~/.local/state/newgate/backups/<sid>/<hash>.bak
4. 写 journal 条目（原路径、备份路径、sha256、pid、时间戳）
5. 原子写入 patch 后的内容（temp → fsync → rename）
6. spawn，等待
7. 恢复：校验当前文件 sha256 == 我们写入的值？
      是 → 直接 rename 备份回去
      否 → 用户/tool 在此期间改过文件！进入冲突处理
8. 删 journal 条目、删备份
```

#### 冲突处理（第 7 步）

这是 inplace 模式最脏的地方，必须想清楚：

| 情况 | 处理 |
| --- | --- |
| 文件未变 | 直接还原 |
| 文件被改，但我们注入的键没被动 | 只回滚我们注入的那几个键（记录了精确的 patch，可以做反向 patch），保留用户的其他修改 |
| 文件被改，且注入的键也被动了 | 不覆盖用户改动。把备份留在 `backups/` 并在 stderr 告知路径，退出码不变 |

因此 **patch 必须记录精确的 JSON Pointer 级别操作和每个键的原值**（包括「原来不存在」这个状态），而不是整文件覆盖。反向 patch 的能力是 inplace 模式的必需品。

#### 格式保真

配置文件是用户手写的，里面有注释和格式。**不要 `JSON.parse` 再 `JSON.stringify` 整个文件**——会把注释和缩进全洗掉。

- `jsonc` / `json5`：用保留格式的编辑库（如 `jsonc-parser` 的 `applyEdits`），只改目标区间的字节。
- `toml`：同理，用支持 edit-in-place 的库（如 `@iarna/toml` 不行，需要 AST 级别的编辑器）。
- `yaml`：用保留注释的 AST（如 `yaml` 包的 Document API）。

这一点在 sandbox 模式下可以放宽（改的是副本，用完就删），但 inplace 模式下是硬要求。

## 4. injection:composite

有序组合多个注入，**全成或全滚**。

```ts
composite([
  configInjection,   // 先改配置文件
  envInjection,      // 再叠 env（env 通常优先级更高，放后面）
])
```

语义：

- `probe()`：所有成员都必须 ok（除非成员标记为 `optional: true`）。
- `plan()`：按序生成子 plan，后面的 plan 可以看到前面 plan 的产物（比如 config 注入产生了 sandbox 目录路径，env 注入需要把这个路径写进 `CLAUDE_CONFIG_DIR`）。
- `apply()`：按序 apply；**第 k 个失败 → 对 k-1..0 逆序 rollback**，然后抛出。
- `rollback()`：一律逆序。

顺序敏感，所以描述符里 composite 的成员列表是有序数组，不是集合。

## 5. 崩溃恢复（Journal）

`SIGKILL`、断电、终端被关掉——正常的 rollback 跑不到。靠 journal 兜底。

### Journal 条目

`~/.local/state/newgate/journal/<session-id>.json`：

```jsonc
{
  "sessionId": "01J...",
  "pid": 12345,
  "startedAt": "2026-08-13T10:00:00Z",
  "tool": "claude",
  "ops": [
    { "op": "backup-restore", "path": "/home/u/.claude/settings.json",
      "backup": "/home/u/.local/state/newgate/backups/01J.../settings.json",
      "sha256After": "…" },
    { "op": "rmdir", "path": "/tmp/newgate-01J..." }
  ]
}
```

写 journal 必须在 apply **之前**并 fsync，否则「已改文件但没记录」就永久失联了。

### 恢复时机

每次 `newgate` 启动时（在做任何事之前）扫描 journal 目录：

- 条目的 pid 还活着 且 进程启动时间匹配 → 是并发的其他 session，跳过。
  （只比 pid 会误判 pid 复用，要一起比进程启动时间。）
- pid 不存在 → 孤儿条目 → 执行恢复，然后删条目，并在 `-v` 下告知用户。

`newgate doctor` 显式列出所有孤儿条目和恢复结果。

### 恢复必须是幂等的

恢复过程本身也可能被打断。每个 op 设计成可重复执行：`rename(backup, path)` 幂等（备份没了说明已恢复），`rm -rf tmpdir` 幂等。

## 6. injection:proxy

Gateway 插件启用时的形态：newgate 不把真实凭证给 tool，而是给它一个本地网关地址。

```
env: {
  ANTHROPIC_BASE_URL: 'http://127.0.0.1:8788/v1/anthropic',
  ANTHROPIC_AUTH_TOKEN: '<per-session 短期令牌>',
}
```

好处：

- **真实 API key 从不进入 tool 进程**，也不会出现在 tool 的日志、崩溃报告、配置备份里。
- **热切换**：session 跑着的时候在 Web 上换 provider，网关立刻改路由，tool 无感知、无需重启。这是 env/config 注入做不到的。
- 统一的用量统计、请求日志、失败重试、多 provider 故障转移。

代价：多一个必须在跑的进程；流式响应要正确透传；协议翻译有损耗。详见 [08-gateway.md](08-gateway.md)。

## 7. 选择顺序与降级

默认策略（可被 `--injection` 或 profile 覆盖）：

```
proxy (若插件启用且网关在跑)
  ↓ 不可用
env (若 tool 支持且 envBeatsConfig)
  ↓ 不可用
composite[config(sandbox), env]
  ↓ sandbox 不可用（tool 无 configDirEnv）
composite[config(inplace), env]
  ↓ 不可用（文件不可写 / 被别的 session 锁住）
报错，退出码 69，并给出具体原因和建议
```

降级过程在 `-v` 下打印。**静默降级是 bug**：用户以为在用 sandbox，实际改了他的真配置，这种事只要发生一次就没人敢用了。

## 8. 测试

| 场景 | 断言 |
| --- | --- |
| env 注入 | 假 tool 打印 env，断言变量存在且干扰变量已删除 |
| sandbox | 原配置文件 mtime + 内容不变；副本内容正确；退出后临时目录消失 |
| inplace 正常 | 退出后文件与运行前逐字节相同（含注释和缩进） |
| inplace 冲突 | 运行中修改文件 → 断言不覆盖用户改动 + 备份路径被提示 |
| composite 部分失败 | 第 2 步失败 → 断言第 1 步被回滚 |
| SIGKILL 恢复 | kill -9 → 再跑一次 newgate → 断言文件已还原 |
| 并发 | 同时起 5 个不同 provider 的 session → 各自 env 正确、互不串 |
| pid 复用 | 构造 pid 相同但启动时间不同的条目 → 断言不误删活跃 session 的现场 |
