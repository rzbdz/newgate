# 05 · 工具描述符与适配

> **重要**：本文档里各 CLI 的具体环境变量名、配置文件路径与字段，是基于当前认知的**草案**，实现前必须逐条对着上游文档/源码核实。文末有核实清单。适配错一个变量名的后果是「静默走了错误的 provider」，比报错更糟。

## 1. 描述符规范

Tool 是**数据**，不是代码。目标：接一个新 CLI ≈ 写一份 JSON。

```ts
interface ToolDescriptor {
  schemaVersion: 1;
  id: string;
  displayName: string;
  aliases?: string[];
  homepage?: string;

  /** 怎么找到可执行文件 */
  resolve: {
    bin: string | string[];        // PATH 里找这些名字，按序
    fallback?: string[];           // 找不到时的候选路径（npm global、~/.local/bin 等）
    versionCmd?: string[];         // ['--version']，用于 doctor 和能力判定
    installHint?: string;          // 找不到时打印给用户的安装命令
  };

  acceptsProtocols: Protocol[];    // 按优先级

  /** env 注入映射 */
  env?: {
    map: EnvMapEntry[];
    unset?: string[];              // 必须从子进程 env 中删除的干扰变量
    beatsConfig: boolean;          // env 优先级是否高于该 CLI 的配置文件
  };

  /** config 注入映射 */
  config?: {
    path: PathSpec;                // 支持 ~ 和平台差异
    format: 'json' | 'jsonc' | 'toml' | 'yaml';
    configDirEnv?: string;         // 有它才能用 sandbox 模式
    sandboxCopy?: string[];        // sandbox 时需要复制的文件/目录白名单
    map: ConfigMapEntry[];
  };

  injections: InjectionKind[];     // 优先级顺序
  capabilities?: {
    supportsModelOverride?: boolean;
    supportsFastModel?: boolean;
    supportsCustomHeaders?: boolean;
    readsConfigOnce?: boolean;     // 只在启动时读配置（决定热切换是否可行）
  };
  passthrough?: {
    stopsAtDoubleDash?: boolean;
    knownSubcommands?: string[];   // 仅用于补全提示，不影响透传
  };
  notes?: string;
}
```

### EnvMapEntry

映射用模板，而不是硬编码：

```jsonc
{ "name": "ANTHROPIC_BASE_URL", "value": "{{endpoint.baseUrl}}" },
{ "name": "ANTHROPIC_AUTH_TOKEN", "value": "{{credential}}", "secret": true },
{ "name": "ANTHROPIC_MODEL", "value": "{{models.primary}}", "omitIfEmpty": true }
```

可用的插值变量：`endpoint.baseUrl`、`endpoint.protocol`、`credential`、`models.primary|fast|reasoning`、`provider.id`、`session.id`、`headers.<name>`。

`secret: true` 的值在所有日志、`--print-env`、`--dry-run` 输出里自动脱敏。

### ConfigMapEntry

```jsonc
{ "pointer": "/env/ANTHROPIC_BASE_URL", "value": "{{endpoint.baseUrl}}" },
{ "pointer": "/model_providers/newgate/base_url", "value": "{{endpoint.baseUrl}}" },
{ "pointer": "/model_provider", "value": "newgate" }
```

`pointer` 用 JSON Pointer 语法（TOML/YAML 也用它表达路径，内部转换）。每个 entry 在 apply 时记录原值（含「原本不存在」），供反向 patch 使用。

## 2. 内置描述符草案

### 2.1 claude — Claude Code

`acceptsProtocols: ['anthropic']`

**env 映射（草案）**

| 变量 | 值 |
| --- | --- |
| `ANTHROPIC_BASE_URL` | `{{endpoint.baseUrl}}` |
| `ANTHROPIC_AUTH_TOKEN` | `{{credential}}` (secret) |
| `ANTHROPIC_MODEL` | `{{models.primary}}` |
| `ANTHROPIC_SMALL_FAST_MODEL` | `{{models.fast}}` |
| `ANTHROPIC_CUSTOM_HEADERS` | 由 `provider.headers` 拼装 |

`unset`: `ANTHROPIC_API_KEY`（与 `AUTH_TOKEN` 并存时行为不确定，统一只用一个，另一个删掉）

**config 映射（草案）**：`~/.claude/settings.json`，格式 json，`/env/*` 下写同名键。`configDirEnv: CLAUDE_CONFIG_DIR`（**需核实**）。

**injections**: `['env', 'composite']` — Claude Code 对 env 支持良好，env 注入应当足够，config 注入作为 env 被覆盖时的兜底。

### 2.2 codex — OpenAI Codex CLI

`acceptsProtocols: ['openai-responses', 'openai']`

**config 映射（草案）**：`~/.codex/config.toml`，TOML。思路是**新增一个专属 provider 块再切过去**，而不是改用户已有的块：

```toml
model_provider = "newgate"

[model_providers.newgate]
name = "newgate"
base_url = "{{endpoint.baseUrl}}"
env_key = "NEWGATE_CODEX_KEY"
wire_api = "responses"      # 或 "chat"，取决于 endpoint.protocol
```

配合 env 注入 `NEWGATE_CODEX_KEY={{credential}}`——**key 走 env，不落盘**，这是 config 注入下保护凭证的标准手法：配置文件里只放「去哪个环境变量取 key」。

`configDirEnv: CODEX_HOME`（**需核实**）。`injections: ['composite']`（config + env 必须一起用）。

### 2.3 opencode

`acceptsProtocols: ['openai', 'anthropic']`

配置文件在 `~/.config/opencode/opencode.json`（**需核实**），provider 定义嵌在 `provider` 字段下。同样采用「新增 newgate 专属 provider 条目 + 切默认」的策略。

`injections: ['composite']`。**整体待核实，优先级排在 claude/codex 之后。**

### 2.4 hermes

**信息不足**。需要你补充：它读什么配置文件、认哪些环境变量、说哪种协议方言。

在拿到信息之前，先用一个**占位描述符**验证「纯数据接入」这条路走得通：

```jsonc
{
  "schemaVersion": 1,
  "id": "hermes",
  "displayName": "Hermes",
  "acceptsProtocols": ["openai"],
  "resolve": { "bin": ["hermes"], "installHint": "TODO" },
  "env": { "map": [], "beatsConfig": true },
  "injections": ["env"],
  "notes": "占位。待补：配置文件路径/格式、env 变量名、协议方言。"
}
```

### 2.5 gemini / 其他

同样按需补。列在这里是为了检验描述符 schema 的表达力够不够——如果加一个 CLI 需要改 schema，说明抽象漏了。

## 3. 通用适配难点

这些是所有 tool 都会碰到的坑，抽象设计要能容纳：

| 难点 | 应对 |
| --- | --- |
| CLI 缓存了上次的 provider / session | 描述符标注 `readsConfigOnce`；必要时注入的同时清理其 session 缓存（需要 `preLaunchHooks`） |
| CLI 自己会写回配置文件 | inplace 模式的冲突处理（[04-injection.md](04-injection.md) §3.2） |
| CLI 有交互式登录态，与 API key 冲突 | 描述符声明 `conflictsWithAuth`，注入前检查并警告 |
| 环境变量优先级低于配置文件 | `env.beatsConfig: false` → 禁止单用 env 注入 |
| Windows 路径与 env 差异 | `PathSpec` 支持按平台分支；`bin` 支持 `.cmd`/`.exe` 后缀探测 |
| 通过 npx/bunx 间接启动 | `resolve.fallback` 支持带参数的启动器形式 |

预留 `preLaunchHooks` / `postExitHooks`（描述符里声明的、newgate 执行的少量内置动作，如「删除某缓存文件」），但**不要**做成任意 shell 命令执行——那会变成安全洞。做成封闭的动作枚举。

## 4. 核实清单（实现前必须逐条确认）

| # | 待核实项 | tool | 置信度 | 怎么核实 |
| --- | --- | --- | --- | --- |
| 1 | `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` 名称与语义 | claude | 高 | 官方文档 |
| 2 | `ANTHROPIC_API_KEY` 与 `AUTH_TOKEN` 同时存在时谁赢 | claude | 低 | 实测 |
| 3 | `CLAUDE_CONFIG_DIR` 是否存在、是否覆盖全部配置 | claude | 中 | 实测 |
| 4 | `settings.json` 的 `env` 块与真实 env 的优先级 | claude | 中 | 实测 |
| 5 | `ANTHROPIC_CUSTOM_HEADERS` 的格式（分隔符） | claude | 低 | 文档 + 实测 |
| 6 | `config.toml` 的 `model_providers` 字段名与 `wire_api` 取值 | codex | 中 | 文档/源码 |
| 7 | `CODEX_HOME` 是否存在 | codex | 中 | 源码 |
| 8 | 配置文件路径与 provider schema | opencode | 低 | 文档 |
| 9 | 全部信息 | hermes | 无 | **需要你提供** |

核实方法建议：写一个 `newgate doctor --probe <tool>`，用一个假的本地 HTTP 服务当上游，实际启动 tool 发一个请求，**观察它到底连了哪里、带了什么头**。这比读文档可靠——文档会过时，抓包不会。这个探针本身也是最好的回归测试。
