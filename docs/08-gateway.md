# 08 · Gateway / Proxy 插件

## 1. 为什么需要它

env/config 注入解决了「启动时用哪个 provider」，但有四件事它做不到：

| 做不到的事 | 网关怎么解决 |
| --- | --- |
| 跑到一半换 provider | 网关改路由，tool 无感知（它连的地址没变） |
| provider 挂了自动切备用 | 网关做故障转移与重试 |
| tool 只说 A 协议，provider 只说 B 协议 | 网关做协议翻译 |
| 看不到花了多少钱、请求长什么样 | 网关是唯一能看到全部流量的位置 |

代价是多一个常驻进程和一层延迟。所以它是**插件，默认关闭**。

## 2. 形态

独立进程 `newgate-gateway`（或 `newgate gateway start`），监听 `127.0.0.1:8788`。

```
tool ──▶ 127.0.0.1:8788/v1/{protocol}/…  ──▶ 路由决策 ──▶ 上游 provider
                     ▲                              │
                     │                              ▼
              per-session token                 用量/日志/成本
```

`injection:proxy` 注入的是网关地址 + **每 session 的短期令牌**，而不是真实 API key。真实 key 只有网关进程持有。

### 路径设计

```
POST /v1/anthropic/v1/messages         → 以 anthropic 协议进入
POST /v1/openai/v1/chat/completions    → 以 openai 协议进入
```

前缀声明「tool 说的是哪种方言」，网关据此解析请求；出口协议由选中的 endpoint 决定，不一致就翻译。

## 3. 协议翻译

翻译矩阵（M1 只做最常用的两格，其余原样透传或报错）：

| 入 \ 出 | anthropic | openai | gemini |
| --- | --- | --- | --- |
| **anthropic** | 透传 | ✅ 需要做 | 后续 |
| **openai** | ✅ 需要做 | 透传 | 后续 |
| **gemini** | 后续 | 后续 | 透传 |

翻译必须覆盖的部分，缺一个就会在真实使用中炸：

- **流式**：SSE 事件序列的语义映射（Anthropic 的 `message_start`/`content_block_delta`/`message_stop` ↔ OpenAI 的 `chat.completion.chunk`）。**必须逐块转发，不能缓冲整个响应**——否则交互体验完全毁掉。
- **工具调用**：tool schema、tool_use / tool_calls、并行调用、tool_result 的对应。这是翻译层最容易出错的地方，也是 AI CLI 最依赖的能力。
- **多模态**：图片 base64 / URL 的格式差异。
- **system prompt**：Anthropic 是顶层 `system` 字段，OpenAI 是 messages 里的 role。
- **停止原因/用量字段**：`stop_reason` ↔ `finish_reason`，`usage` 字段名与口径（是否含 cache token）。
- **错误**：上游错误码与错误体的格式转换，且**不要吞掉原始错误信息**——加一个 `x-newgate-upstream-error` 头带原文，排查时是救命的。

翻译层要有**双向 golden 测试**：录制真实上下行报文，断言翻译结果逐字段正确。这一层没有测试就等于没写。

## 4. 路由策略

```jsonc
{
  "routes": [
    {
      "match": { "tool": "claude", "model": "primary" },
      "policy": "failover",
      "targets": [
        { "provider": "anthropic-official", "weight": 1 },
        { "provider": "kimi", "weight": 1 }
      ],
      "retry": { "maxAttempts": 2, "on": [429, 500, 502, 503, 529], "backoffMs": 500 }
    }
  ]
}
```

策略：`single` / `failover`（按序）/ `weighted`（加权随机）/ `cheapest`（按 tag 与价格表）。

### 重试的红线

**只重试确定没有产生副作用的失败**：连接失败、429、5xx 且**尚未开始流式输出**。一旦已经往 tool 吐了第一个 token，就不能重试到另一个 provider——那会产生一半 A 一半 B 的响应。这一条要写进代码注释，很容易被后来者改错。

## 5. 观测

- **用量**：按 session / tool / provider / 模型 聚合 input/output/cache token，按可配置的价格表折算成本。价格表放配置里，不硬编码（价格会变）。
- **请求日志**：默认**只记元数据**（时间、模型、token 数、耗时、状态码）。记录请求体是可选开关，且默认对内容做截断 + 明确警告——那是用户的源代码和对话内容。
- **实时流**：通过 BE 的 SSE 转给 Web。

## 6. 安全

- 只监听 loopback。
- 每 session 一个短期 token，session 结束即失效；网关拒绝无 token 请求（防止本机其他程序白嫖你的 API key）。
- 网关持有的真实 key 在内存中用 `Secret` 包装，不写日志、不进 core dump 友好路径。
- 提供 `--redact` 模式：日志里对疑似密钥/邮箱/路径做脱敏。

## 7. 与注入层的关系

网关不改变注入抽象，它只是**第四种 Injection 实现**（[04-injection.md](04-injection.md) §6）。这意味着：

- 不启用网关时，代码路径完全不经过它，没有性能与可靠性代价。
- 启用后，`newgate --injection proxy claude` 就切过去了，其他一切不变。
- 网关不可用时按降级链回落到 env 注入，并明确告知用户（不静默降级）。

这个边界要守住：**不要让网关的存在污染 core 层的类型**。core 只知道有一种叫 `proxy` 的注入，不知道网关内部是怎么路由的。
