# 13 · 痛点挖掘

> 你已经点名的痛点：配置切换繁琐、多工具格式不一、多模型槽位、代理接管、诊断困难。
> 这一篇挖掘**尚未被覆盖**的痛点。按「有多疼 × 现有方案有多烂」排序，标注是否值得做。

## P0 · 值得当作核心卖点

### P0-1 「我明明切了，为什么还是走老的」

**最高频的故障，且用户完全无法自查。**

原因可能是：shell 里残留的 `ANTHROPIC_API_KEY`、配置文件优先级高于 env、tool 缓存了 session、改的是全局配置但项目里有覆盖、改完没重启、改的是 A 工具的配置但跑的是 B。

现有工具的表现：切换成功的绿色对勾 → 用户以为好了 → 实际没生效 → 账单出来才发现。

**newgate 的解法**（这是最该打磨的体验）：

```bash
$ newgate status --explain
claude → kimi (provider)
  ├ provider  来自  ~/.config/newgate/defaults.json  (--set-provider 于 2h 前设置)
  ├ endpoint  https://api.moonshot.cn/anthropic  [anthropic 协议]
  ├ 注入方式  env  (config 沙箱不可用：CLAUDE_CONFIG_DIR 未验证)
  ├ 将注入    ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL
  └ ⚠ 将删除  ANTHROPIC_API_KEY  (你的 shell 里有这个变量，会覆盖我们的注入)
```

更强的是**事后验证**：gateway 或探针能确认「上一次运行确实打到了 kimi」，而不只是「我们确实写了配置」。

> **写入成功 ≠ 生效。** 只声称前者的工具，把最难的一半留给了用户。

### P0-2 花了多少钱，不知道

多 provider 切换的直接动机就是成本。但切完之后没人知道省了没有。

痛点细分：

- 单次会话花了多少（跑完一个大重构，花了 3 块还是 30 块？）
- 哪个 role 在烧钱（往往是意料之外的那个，比如给每轮对话生成标题的模型）
- 哪个项目/仓库烧钱
- 预算告警（今天超过 X 就提醒/自动降级到便宜 provider）

Gateway 是唯一能看到全部流量的位置，天然具备这个能力。**自动降级**尤其有价值：预算过半自动把 `primary` 从 opus 切到 sonnet，这是「省钱」从被动统计变成主动控制。

### P0-3 上下文/会话跟着 provider 一起丢

切了 provider，正在进行的会话怎么办？

- 有的工具会话与模型绑定，切完历史就断了
- 切到上下文窗口更小的 provider，长会话直接失败
- 切完想切回来，找不到之前的会话

**能做的**：切换前检查目标 provider 的上下文窗口是否够当前会话用，不够就提前警告（而不是跑到一半 400）。这是 gateway 有 token 计数能力后的自然延伸。

### P0-4 团队共享配置

一个人配好了 5 个 provider、3 个 group，团队其他人要重来一遍。

现有做法是发个 JSON 互相拷，但里面有 key，不敢发。

**能做的**：`newgate export --template` 导出**结构但不含凭证**的模板，队友导入后只需填自己的 key（UI 上明确列出「你需要提供这 3 个 key」）。配合项目级 `.newgate.json` 引用 provider id（不含凭证，[06-config-schema.md](06-config-schema.md) §6），团队能统一「这个仓库用什么模型」而各自持有自己的额度。

### P0-5 provider 挂了 / 限流，手动切太慢

国内中转服务不稳定是常态。429、502、超时，用户要手动切到备用。

Gateway 的故障转移解决这个，但要做对：

- 熔断（连续失败 N 次就摘掉一段时间，不要每次请求都去撞）
- **只在未开始流式输出前重试**（[08-gateway.md](08-gateway.md) §4.2 的红线）
- 转移后要**明显告知**——用户需要知道「你现在实际在用备用 provider」，否则质量变化会让他困惑

## P1 · 明显价值，排在核心之后

### P1-1 配置的版本历史与回滚

「昨天还好好的，今天不行了，我改了啥？」

所有配置变更进 append-only 的历史，支持 `newgate history` 和 `newgate rollback <n>`。实现成本低（Config Store 每次写入前存一份快照），价值高。

### P1-2 provider 能力探测与不匹配预警

用户把 `claude` 切到一个不支持工具调用的便宜 provider → Claude Code 直接不能用，报一堆看不懂的错。

**能做的**：探活时不只测「通不通」，还测能力矩阵（工具调用、流式、视觉、上下文长度、并行工具调用）。切换时若目标能力不足以支撑该 tool，提前警告。

这需要一组标准探测请求，成本不高（每个 provider 几次调用，结果缓存）。

### P1-3 多账号 / 额度轮换

同一个 provider 有多个 key（多个账号、多个子账号），想轮换用或按额度用。

Provider 支持 `credentials: SecretRef[]` + 轮换策略（round-robin / 按剩余额度 / 失败切换）。gateway 层实现，对 tool 透明。

### P1-4 首次上手成本

装完之后面对空配置，要填 baseUrl、key、模型名——每一项都可能填错，且错了只表现为一个 401。

**能做的**：

- 从现有配置**导入**：扫描 `~/.claude`、`~/.codex` 等，把已有的 provider 直接导进来（用户往往已经配好了，只是没有集中管理）。这是最快的冷启动路径。
- 内置常见 provider 的模板（只填 key 即可）。
- 填完立刻探活并给出明确结果，而不是等到第一次用才报错。

> cc-switch 的 50+ 预设 + 自动导入现有配置，正是它上手快的原因。这块不能落后，但也不该是差异化重点——它可复制。

### P1-5 环境噪音的自动清理

用户的 shell rc 里可能有历史遗留的 `export ANTHROPIC_BASE_URL=...`，与 newgate 打架。

`newgate doctor` 应该扫描 shell rc 文件（只读、不改），把冲突项列出来并给出清理建议。**不要自动改用户的 rc**，但要让他知道问题在哪。

### P1-6 「这个模型到底能不能用」

配置里写了个模型名，provider 实际不提供。表现为 404，但错误信息往往含糊。

探活时拉一次 `/models` 缓存下来，配置模型名时提供补全 + 校验。填错立刻标红，而不是运行时才炸。

## P2 · 有价值但可以后置

| 痛点 | 说明 |
| --- | --- |
| 请求/响应的本地记录与回放 | 排查「模型为什么这样回答」，以及回归测试。隐私敏感，默认关闭 |
| A/B 对比 | 同一个 prompt 同时发给两个 provider，对比质量与成本。适合选型阶段 |
| 按项目自动切换 | 进入某目录自动用某 provider（`.newgate.json` 已覆盖，可再加 shell hook） |
| 定时策略 | 白天用贵的、夜里用便宜的；工作日/周末不同 |
| 敏感代码不出境 | 按目录/文件模式匹配，命中则强制走本地模型。合规场景刚需 |
| 与 direnv / mise 集成 | 已有工具链的用户希望复用 |
| 用量导出 | CSV/JSON 导出给公司报销或成本分析 |

## P3 · 想清楚了再碰

| 痛点 | 为什么谨慎 |
| --- | --- |
| 自动选择「最优」provider | 「最优」难以定义，猜错让用户失去信任。至多做**建议**，不做自动 |
| 缓存/去重请求 | 语义正确性风险高，AI CLI 的请求很少完全重复 |
| 修改 prompt（注入系统提示、压缩上下文） | 一旦动了内容，出问题时用户无法判断是模型问题还是 newgate 问题。**破坏可信度的成本极高** |
| 托管密钥的云端同步 | 变成安全责任主体，与 [09-security.md](09-security.md) §6 的边界冲突 |

## 反痛点：newgate 自己可能造成的新痛

诚实列一下，这些要在设计里主动规避：

| 自造的痛 | 规避 |
| --- | --- |
| 多一层，出问题时多一个嫌疑人 | `NEWGATE_DISABLE=1` 一键旁路；诊断默认输出「排除 newgate 因素」的验证步骤 |
| 启动变慢 | 热路径不联网、不启 daemon、不调 LLM；目标启动开销 < 50ms |
| 改坏了用户的配置文件 | 优先 sandbox / L1 / L3 这些不改原文件的路径；inplace 是最后手段 |
| MITM 引入安全焦虑 | CA 不进系统信任库、作用域限于子进程、劫持名单可审计（[11-proxy-native.md](11-proxy-native.md) §3.2） |
| LLM 生成的适配器出错 | 探针实测裁决，未验证的标记清楚，绝不假装成功（[10-llm-native.md](10-llm-native.md) §1） |
| 概念太多学不会 | `newgate claude` 必须零配置可用；provider/role/group/injection 全是渐进暴露 |

**最后一条最容易被忽视，但可能是最致命的。** 这份文档里定义了 provider、endpoint、profile、role、group、injection、tool、session 八个概念——对我们是清晰的抽象，对新用户是一堵墙。

对策：**默认路径上一个概念都不出现。**

```bash
newgate claude                    # 不知道 provider 是什么也能用
newgate use kimi --target claude  # 只需要知道 "用哪个"
```

profile / group / injection / endpoint 全部是「当你需要时才出现」的高级功能。文档也要分两层：Getting Started 只讲 provider 和 tool，其余进 Advanced。
