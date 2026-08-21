# 03 · CLI 规范

## 1. 语法

```
newgate [newgate-opts] <tool> [tool-args...]         # 包装启动
newgate [newgate-opts] -- <tool> [tool-args...]      # 显式分隔（消歧用）
newgate-<preset> <tool> [tool-args...]               # argv0 分发，见 §1.5
newgate <subcommand> [args...]                       # 控制面操作
newgate [newgate-opts]                               # 无 tool：报错并给提示
```

## 1.5 argv0 分发（多调用二进制）

程序启动时先读 `argv[0]` 的 basename：

| basename | 等价于 |
| --- | --- |
| `newgate` | 正常解析 |
| `newgate-<preset>` | `newgate --preset <preset> …` |

```bash
newgate preset install toy professional free
# → 在与主二进制同目录（或 ~/.local/bin）建三个符号链接

newgate-toy claude --resume abc     # ≡ newgate --preset toy claude --resume abc
```

规则：

- preset 名从 basename 第一个 `-` 之后取，**不再切分**（preset 名可含 `-`：`newgate-cn-cheap` → preset `cn-cheap`）。
- basename 里的 preset **不存在时立刻报错**，退出码 65。绝不静默回落到默认 preset——那正是「切了没生效」这类故障的来源。
- 同时给 `argv[0]` 和 `--preset` 且**不一致**时报错；一致则放行。
- Windows 无符号链接习惯，`preset install` 改为生成 `.cmd` 转发脚本；安装后校验 PATH 中该名字确实解析到我们的 shim。
- `newgate preset uninstall <name>` 移除；`newgate preset ls` 列出已安装的链接及其解析目标。

**递归保护**：注入 `NEWGATE_DEPTH`，超过阈值（默认 2）时按 `NEWGATE_DISABLE` 处理，避免工具内部再调 newgate 造成套娃。

## 2. 参数切分规则（透传的核心）

> 需求原文：`newgate --provider xxx claude --help`、`newgate --provider xxx claude --resume xxx` 必须原样透传。
> 这意味着 **不能用普通的 argv parser 一把梭**，必须先切分再解析。

### 规则

1. 从左往右扫描 argv。
2. 遇到 `--`：其后**第一个 token 是 tool**，再之后全是透传参数，扫描结束。
3. 遇到 newgate 已知选项：按其 arity 消费值（`--provider kimi` 消费两个 token，`--provider=kimi` 消费一个）。
4. 遇到 **第一个非选项 token**：它是 tool，**它之后的所有 token 一律原样透传，不再解析**。
5. 遇到未知选项且尚未确定 tool：报错（不是透传）——因为此时还没进入 tool 的领地，未知选项一定是拼错了。

### 逐例验证

| 命令 | tool | newgate 收到 | 透传给 tool |
| --- | --- | --- | --- |
| `newgate claude` | claude | — | — |
| `newgate claude --help` | claude | — | `--help` ✅ 规则 4 |
| `newgate --help` | — | `--help` | — ✅ newgate 自己的帮助 |
| `newgate --provider kimi claude --resume abc` | claude | `--provider kimi` | `--resume abc` |
| `newgate --provider=kimi claude -p "hi"` | claude | `--provider=kimi` | `-p "hi"` |
| `newgate --provider kimi claude --provider zzz` | claude | `--provider kimi` | `--provider zzz` ✅ 后面的归 tool |
| `newgate -- claude --help` | claude | — | `--help` |
| `newgate --provider deepseek -- codex exec "x"` | codex | `--provider deepseek` | `exec "x"` |
| `newgate --bogus claude` | — | 错误 | — ✅ 规则 5 |
| `newgate claude -- --weird` | claude | — | `-- --weird` ✅ 规则 4 之后不解析 |

**歧义点**：`newgate --provider claude claude`——`--provider` 明确消费一个值，所以第一个 `claude` 是 provider id，第二个是 tool。行为确定，但 UI 上 `newgate status --explain` 应该把它讲清楚。

**歧义点**：tool 名与子命令重名。见 §4。

### 实现要求

argv 切分必须是独立的、可单测的纯函数：

```ts
function splitArgv(argv: string[], knownOpts: OptionSpec[]): {
  newgateArgs: string[];
  tool: string | null;
  passthrough: string[];
  error?: SplitError;
}
```

不要把它埋进 commander/yargs 里。先切分，再把 `newgateArgs` 丢给 parser。

## 3. newgate 自己的选项

| 选项 | 简写 | 说明 |
| --- | --- | --- |
| `--provider <id>` | `-P` | 本次使用的 provider |
| `--profile <id>` | | 本次使用的 profile（与 `--provider` 互斥，同时给 → 报错） |
| `--preset <id>` | | 本次使用的 preset（整套 role 绑定），见 [16-core-thesis.md](16-core-thesis.md) §3 |

| `--role <id=provider/model>` | | 覆盖单个 role，可重复 |
| `--remote <name>` | | 对远端执行（见 [12-frontends.md](12-frontends.md) §4） |
| `--model <slot=id>` | | 覆盖模型槽位，可重复：`--model primary=gpt-4o` |
| `--injection <kind>` | | 强制注入方式：`env` / `config` / `composite` / `proxy` |
| `--endpoint <url>` | | 临时覆盖 base URL（调试用，不落盘） |
| `--dry-run` | `-n` | 只打印注入计划，不 apply、不 spawn |
| `--print-env` | | 打印将注入的 env（key 脱敏），不 spawn；`--print-env=raw` 输出 shell `export` 行 |
| `--explain` | | 打印解析过程与每个字段的来源（provenance） |
| `--no-rollback` | | 保留注入现场（调试用，会警告） |
| `--cwd <dir>` | | 指定 tool 的工作目录及项目级配置查找起点 |
| `--verbose` | `-v` | 详细日志，可叠加 `-vv` |
| `--quiet` | `-q` | 只输出 tool 的输出 |
| `--json` | | 结构化输出（子命令用） |
| `--version` | | 版本 |
| `--help` | `-h` | 帮助 |

### 默认值设置（你给的形式）

```bash
newgate --set-provider aa --target claude
newgate --set-provider bb --target hermes
newgate --set-provider aa                  # 省略 --target → 设置全局默认
```

`--set-provider` 是**动作型选项**：出现时不启动任何 tool，执行完打印结果并退出。

等价的子命令形式（推荐在文档里同时给出，更好发现）：

```bash
newgate use aa --target claude
newgate use --list
```

两种形式共存，`--set-provider` 为主（符合你的原始设计），`use` 是别名。

## 4. 子命令

**保留字问题**：`newgate claude` 里 `claude` 是 tool，`newgate list` 里 `list` 是子命令。规则：

1. 保留子命令名是**固定的封闭集合**（下表），优先匹配。
2. Tool id / alias **不允许**与保留字冲突，注册时校验并拒绝。
3. 万一必须用冲突的名字：`newgate run <tool> [args...]` 显式走包装路径。

| 子命令 | 说明 |
| --- | --- |
| `run <tool> [args…]` | 显式包装启动（等价于 `newgate <tool>`） |
| `use <provider> [--target <tool>]` | 设默认值（`--set-provider` 的别名） |
| `ls` / `list` | 概览：providers、tools、defaults |
| `provider add/ls/rm/edit/test <id>` | provider 增删改查；`test` 发一次探活请求 |
| `tool ls/show/add/rm <id>` | tool 描述符管理 |
| `profile add/ls/rm <id>` | profile 管理 |
| `preset ls/show/add/rm <id>` | preset 管理 |
| `preset install/uninstall <name…>` | 安装/移除 `newgate-<preset>` 分发链接（见 §1.5） |
| `preset set <preset> <role> <provider>/<model>` | 改 preset 里的单个 role 绑定 |
| `discover <hint>` | 发现流水线：自动接入一个新工具（见 [10-llm-native.md](10-llm-native.md)） |
| `explain` / `diagnose [--ai]` | 诊断当前问题 |
| `tui` | 启动 TUI |
| `remote add/ls/rm/open <name>` | 远端管理 |
| `mcp serve` | 以 MCP server 形式暴露能力 |
| `ca show/regenerate/trust-check` | 透明拦截用的本地 CA（见 [11-proxy-native.md](11-proxy-native.md) §3.2） |
| `history` / `rollback <n>` | 配置变更历史与回滚 |
| `status [--explain]` | 当前上下文会解析成什么；带 `--explain` 打印 provenance |
| `doctor` | 环境体检：tool 可执行文件、配置文件可写性、残留 journal、端口占用 |
| `serve [--port]` | 启动 BE daemon |
| `web [--open]` | 启动 BE + 打开前端 |
| `gateway start/stop/status` | 网关插件 |
| `sessions` | 列出活跃 session |
| `import` / `export` | 配置导入导出（导出默认脱敏，`--with-secrets` 显式解封） |
| `completion <shell>` | 生成 shell 补全脚本 |

### Shell 补全的特殊性

补全必须知道：第一个非选项 token 之后不要再补 newgate 的选项，而应该**委托给 tool 自己的补全**（如果它有）。至少要做到「不给出误导性的 newgate 选项补全」。

## 5. 输出与退出码

### 退出码

| 码 | 含义 |
| --- | --- |
| 子进程的退出码 | **成功 spawn 后一律透传**，这是默认情况 |
| `64` | 用法错误（argv 解析失败、互斥选项） |
| `65` | 配置错误（schema 不合法、provider 不存在） |
| `69` | 依赖不可用（tool 可执行文件找不到、网关未启动） |
| `70` | newgate 内部错误 |
| `77` | 密钥解析失败 / 权限不足 |
| `78` | 协议协商失败（provider 与 tool 无共同协议） |

子进程被信号杀死时，newgate 用 `128 + signum` 退出，与 shell 惯例一致。

**已知歧义**：如果 tool 自己就返回 64，无法区分是谁的 64。所以 newgate 自己的错误一律**同时**往 stderr 打印带 `newgate:` 前缀的信息，脚本可据此区分。

### 输出约定

- newgate 自己的所有输出走 **stderr**，stdout 完全留给 tool。这样 `newgate claude -p "x" | jq` 能正常工作。
- 例外：`--print-env`、`--json`、`--dry-run` 等**以输出为目的**的调用，结果走 stdout。
- 默认极简：成功启动时不打印任何东西（用户敲的是 `newgate claude`，不是想看 newgate 的开屏动画）。`-v` 才打印解析摘要。

## 6. 环境变量

| 变量 | 说明 |
| --- | --- |
| `NEWGATE_HOME` | 覆盖配置根目录 |
| `NEWGATE_PROVIDER` | 默认 provider（优先级低于命令行） |
| `NEWGATE_PROFILE` | 默认 profile |
| `NEWGATE_INJECTION` | 默认注入方式 |
| `NEWGATE_NO_DAEMON=1` | 不联系 BE |
| `NEWGATE_LOG_LEVEL` | `error|warn|info|debug|trace` |
| `NEWGATE_DISABLE=1` | **完全透传**：直接 exec tool，不做任何注入。用于排查「是不是 newgate 搞的鬼」 |

`NEWGATE_DISABLE` 值得单独强调：任何包装器都必须提供一键旁路，否则用户在出问题时会直接卸载你。

## 7. 递归保护

`newgate claude` 启动的 claude 里如果又调用了 `newgate`（比如 tool 的某个 hook），要避免无限套娃和注入叠加。做法：注入 `NEWGATE_SESSION_ID` 和 `NEWGATE_DEPTH`，深度超过阈值（默认 2）时警告并按 `NEWGATE_DISABLE` 行为处理。
