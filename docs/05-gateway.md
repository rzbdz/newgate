# Gateway 数据面

## 1. 请求路径

```text
HTTP request
  -> identify Agent/profile/dialect
  -> resolve role candidate chain
  -> run route hooks
  -> rewrite model and schema
  -> run request hooks
  -> forward candidate
  -> observe first byte and result
  -> fail over when allowed
  -> audit response and stream to client
```

请求体不做整体 JSON decode/encode。Gateway 使用 byte splice 原语只修改目标字段，
保持大整数、未知键、字段顺序和原始表示。

## 2. 方言

Gateway 支持 Anthropic `/v1/messages` 与 OpenAI
`/v1/chat/completions`。Provider 可分别配置方言入口。探活和真实转发使用相同
URL 选择规则，避免“probe 绿但真实请求 404”。

## 3. Fallback

链上每个候选有独立健康观测。连接失败、允许转移的状态码或首字节超时会记录失败，
再尝试下一候选。已开始向客户端写响应后不能换站。

Route headers 和日志记录实际 profile、链和最终 provider/model。Fallback 不得
静默发生。

## 4. Special treatment

上游或客户端怪癖通过插件注册：

- `Match` 决定适用范围；
- `Why` 解释存在原因；
- `Apply` 做 byte-level 改写并返回 notes；
- `Before`/`After` 表达具名顺序。

插件失败默认 fail-open；任何成功改写都必须输出 notes。用户可用
`newgate st` 查看并单独关闭插件。

## 5. 观测与取证

Metrics 统计请求、链失败、failover、插件改写和 count_tokens 路径。上游 4xx 会
保存 client-sent、we-sent、upstream-said 三份证据，便于证明代理是否改变语义。
