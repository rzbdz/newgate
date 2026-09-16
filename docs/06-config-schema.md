# 06 · 配置存储与 Schema

## 1. 磁盘布局

遵循 XDG 规范；`NEWGATE_HOME` 可整体覆盖（此时全部子目录挂在它下面，方便测试和便携安装）。

```
~/.config/newgate/                 # 配置：用户编辑的，可进 dotfiles 仓库
  config.json                      # 全局设置
  providers.json                   # provider 列表
  profiles.json                    # profile 列表
  defaults.json                    # tool -> provider|profile 绑定
  tools/                         # 用户自定义/覆盖的 tool 描述符
    hermes.json
  secrets.json                     # 仅当密钥后端为 file 时存在，权限 0600
  health.json                      # daemon 全局 binding 健康/熔断表，自动维护
  probe-capabilities.json          # 已学会的方言/quirk，避免重复探测耗费 token

~/.local/state/newgate/            # 状态：机器本地，不应同步
  journal/<session-id>.json        # 崩溃恢复日志
  backups/<session-id>/            # inplace 注入的备份
  sessions.json                    # 活跃会话
  daemon.json                      # BE 监听地址 + token，0600
  logs/newgate.log

~/.cache/newgate/                  # 缓存：可随时删
  probes.json                      # provider 探活结果
  tool-versions.json

<project>/.newgate.json            # 项目级覆盖（向上查找，就近优先）
```

**分离原则**：`config/` 可以放进 dotfiles 同步（除了 `secrets.json`），`state/` 不能。所以密钥默认不放 `config/`——见 §5 和 [09-security.md](09-security.md)。

## 2. config.json

```jsonc
{
  "schemaVersion": 1,
  "defaultProvider": "anthropic-official",
  "defaultInjection": "auto",           // 'auto' = 按 04-injection §7 的降级链
  "daemon": { "enabled": true, "port": 8787, "host": "127.0.0.1", "autoStart": false },
  "gateway": { "enabled": false, "port": 8788 },
  "secrets": { "backend": "keychain" }, // 'keychain' | 'file' | 'env-only'
  "telemetry": { "enabled": false },
  "log": { "level": "info", "redactSecrets": true }
}
```

## 3. providers.json

```jsonc
{
  "schemaVersion": 1,
  "providers": [
    {
      "id": "anthropic-official",
      "displayName": "Anthropic 官方",
      "endpoints": [
        { "protocol": "anthropic", "baseUrl": "https://api.anthropic.com", "default": true }
      ],
      "credential": "keychain:newgate/anthropic-official",
      "models": { "primary": "claude-opus-5", "fast": "claude-haiku-4-5-20251001" },
      "tags": ["official"]
    },
    {
      "id": "kimi",
      "displayName": "Moonshot Kimi",
      "endpoints": [
        { "protocol": "anthropic", "baseUrl": "https://api.moonshot.cn/anthropic" },
        { "protocol": "openai",    "baseUrl": "https://api.moonshot.cn/v1", "default": true }
      ],
      "credential": "env:MOONSHOT_API_KEY",
      "models": { "primary": "kimi-k2-0711-preview" },
      "tags": ["cn", "cheap"]
    },
    {
      "id": "local-vllm",
      "endpoints": [{ "protocol": "openai", "baseUrl": "http://127.0.0.1:8000/v1" }],
      "credential": "plain:not-needed",
      "models": { "primary": "qwen3-32b" },
      "tags": ["offline"]
    }
  ]
}
```

> 上面的 baseUrl 和模型 id 是**示意**，实现时按各家实际文档填，不要照抄。

### 3.1 已落地的形态（M1）

上面那个 `endpoints: [...]` 数组是**设计目标**，当前实现是它的最小子集：
`providers` 是一个 map，每个 provider 一条 `base_url` + 一个 `protocol`
（见 `domain.Provider`）。M1 只需要「两种方言分家」这一种情况，所以先加
一个可选字段而不是马上数组化：

```jsonc
{
  "providers": {
    "ark": {
      "base_url": "https://ark.cn-beijing.volces.com/api/coding/v3", // openai 方言
      "anthropic_url": "https://ark.cn-beijing.volces.com/api/coding", // anthropic 方言，写法同 ANTHROPIC_BASE_URL
      "protocol": "openai",
      "api_key": "…"
    }
  }
}
```

`anthropic_url` 为空 = 两种方言同一个 base（聚合网关的老样子）。为什么
需要它、按什么规则选 base，见 docs/08 §3.1。第三个端点真出现时再谈
`endpoints` 数组化。

## 4. defaults.json

对应需求「每个 cli 有 default persist profile」：

```jsonc
{
  "schemaVersion": 1,
  "byTool": {
    "claude":   { "ref": "provider:anthropic-official", "updatedAt": "2026-08-13T10:00:00Z" },
    "codex":    { "ref": "provider:kimi" },
    "hermes":   { "ref": "profile:hermes-cheap" }
  }
}
```

`ref` 带类型前缀，避免 provider 和 profile 重名时的歧义。

`newgate --set-provider aa --target claude` 就是往这里写一条，走原子替换 + 文件锁。

## 5. 密钥引用（SecretRef）

配置文件里**永远存引用，不存明文**（`plain:` 除外，且带警告）。

```
keychain:<service>/<account>     OS 钥匙串（macOS Keychain / Windows Credential Manager / libsecret）
env:<VAR_NAME>                   从环境变量读
file:<path>                      从文件读（trim 尾部换行），要求权限 <= 0600
cmd:<argv-json>                  执行命令取 stdout，如 cmd:["op","read","op://vault/kimi/key"]
store:<id>                       newgate 自己的 secrets.json（backend=file 时）
plain:<value>                    明文，仅用于本地无鉴权服务；写入时警告
```

解析在**内存**中完成，结果包成 `Secret` 类型：

```ts
class Secret {
  toString() { return '***'; }          // 防止误打印
  toJSON()   { return '***'; }
  reveal(): string;                     // 唯一取明文的口子，调用点可审计
}
```

所有日志、错误信息、`--dry-run`、`--print-env` 默认走 `toString()`。只有 `injection.apply()` 构造子进程 env 时调 `reveal()`。

`cmd:` 有明显的执行风险（配置文件被篡改 = 任意命令执行）。要求：只能从用户自己的 config 目录读取该字段，且 `newgate import` 导入外部配置时**默认剥离所有 `cmd:` 引用**并提示。

## 6. 项目级配置

从 `--cwd`（默认 `process.cwd()`）向上查找，遇到第一个 `.newgate.json` 就停（不合并多层，避免心智负担）。

```jsonc
{
  "provider": "local-vllm",
  "byTool": { "claude": "provider:kimi" },
  "injection": "env"
}
```

**安全考量**：`.newgate.json` 会随仓库分发，clone 别人的仓库就自动生效有风险。规则：

- 项目级配置**只能引用**已存在的 provider id，不能定义新 provider，不能带凭证，不能带 `cmd:`。
- 首次在某个目录使用项目级配置时提示一次（可 `--trust` / `newgate trust .` 记住）。

## 7. 并发与原子性

所有写操作：

```
1. flock(<file>.lock, LOCK_EX)  超时 5s
2. 读当前内容，校验 schemaVersion
3. 应用变更
4. 写 <file>.tmp.<pid>  → fsync → rename(<file>)   (同目录 rename 保证原子)
5. fsync 目录项
6. unlock
```

读操作不加锁（rename 原子性保证读到的一定是完整的旧版或新版）。

## 8. Schema 校验与迁移

- 所有配置文件用 JSON Schema 校验（`schemaVersion` 必填）。校验失败**不静默忽略字段**：报错并指出具体路径。
- 提供 `newgate config validate`。
- 提供 `$schema` 字段导出，让编辑器有补全：`newgate schema > newgate.schema.json`。
- 版本迁移：`store/migrations/v1-to-v2.ts`，启动时自动迁移并先备份到 `state/backups/config-migration-<ts>/`。

## 9. 模块槽位注册表：omo-slots.json

**模块贡献的角色键**落在这个文件里（`~/.config/newgate/omo-slots.json`）。
它是「omo 有哪些 intra-agent 槽位」这条知识的唯一出处：core 不 hard-code
槽位名，daemon 通过 `core/roleprov` 读它把 `omo-sisyphus` / `cat-deep` 注册成
动态角色（docs/18 §1.3）。改了它 1 秒内生效（watcher 的指纹含这个文件）。

```jsonc
{
  "version": 1,
  "source": "omo",
  "updated": "2026-09-16T09:12:00+08:00",
  "mode": "current",                  // current（现状，默认）| suggested（建议）
  "overrides": { "omo-sisyphus": "@heavy" },   // 人手写的覆盖，优先级最高
  "slots": [
    {
      "key": "omo-sisyphus",          // profile 里写的键名
      "kind": "agent", "name": "sisyphus",
      "was": "smt/gpt-5.6-sol",       // 接管前的真实模型名
      "was_fallbacks": ["smt/..."],   // 接管前的 fallback 列表（留档，便于还原）
      "variant": "max",               // 接管前的强度标记
      "current": "normal",            // 现状：老版本会算出来的档位
      "suggested": "heavy",           // 建议：模型体格 + variant 强度
      "default": "normal",            // 实际生效（overrides > mode 决定的）
      "why": "variant=max 上调一级（normal → heavy）"
    }
  ]
}
```

规则：

- **缺省 `current`**：接管不改行为——老版本把每个槽位按体格翻成档位，`current`
  就是那个结果，`default` 默认等于它。
- 显式写进 profile 的键**赢过注册表**（profile 是最强的意图表达）。
- `@引用` / 「档位名」/「provider/模型」三种值都支持，语义见 docs/18 §1.2。
- 释放接管（`newgate off opencode`）清空 `slots` 但**保留 `overrides`**——
  键没了，用户手写的意图不该跟着丢。
- 接管时若原文件已被改写过，「接管前是什么模型」从 `backups/original/` 找回，
  所以反复接管不会丢信息。

```bash
newgate omo                          # 列槽位：接管前模型 / 现状 / 建议 / 生效
newgate omo use omo-sisyphus @heavy  # 改一个槽位的归属
newgate omo mode suggested           # 全部按建议（觉得不好再 mode current）
newgate omo explain omo-sisyphus     # 这个键现在展开成哪条链、为什么
```
