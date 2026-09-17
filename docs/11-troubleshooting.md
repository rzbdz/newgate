# 故障排查

## 1. reasoning_content 400

先看日志是否出现真实回填、占位符或 tool-loop rebase notes。再检查
`dump/err-400-*` 的 client-sent、we-sent、upstream-said：

- 客户端没带，但 we-sent 有真实值：cache 命中；
- we-sent 是占位符：cache 未命中；
- we-sent 缺字段：插件未匹配或被关闭；
- 上游仍拒绝：检查方言和 model 是否属于严格 thinking mode。

**这类 400 不会（也不该）把 binding 摘牌**（2026-09-17 起）：它是请求形状
问题，同一份 body 换哪个 provider 都一样错，跟这个 provider「能不能用」无
关。`newgate breaker` 只会显示真正的可用性熔断；如果这条 binding 出现在
`newgate breaker` 里且 `reason` 不是"真实流量连续失败"以外的可信原因，
先看 `newgate metrics` 里的 `breaker.skipped.shape_error` 是不是在涨——涨
说明这类 400 正在发生但已经不计入熔断了，属于预期。

实测过一次确定性复现：同一份真实 body（带完整历史）连发多次都 400，但
把 reasoning_content 全换成真实文本 / 全部删掉都不影响结果——根因不在
"补的内容对不对"，而在对话**尾部形状**（最后一轮如果只有 `tool_result`
没有跟着一条新的用户指令，DeepSeek 的严格校验会报同一个误导性文案）。
这条根因目前**未修**，另立计划处理；先止血（不熔断）能防止误伤其他
provider。

## 2. Probe 绿但请求 404 / 400

Provider 可能只配置了声明协议入口。分别测试 Anthropic `/v1/messages` 和
OpenAI `/v1/chat/completions`，检查 `anthropic_url` 与通用 base URL。

**「probe 绿、真实流量 400/被摘牌」的另一种成因**：probe 发的是最小请求
（1 条消息、`max_tokens:4`、无 tools、无历史），探不到只在长对话/思考模式
下才触发的上游校验（reasoning_content 就是一例）。这不是 bug，是 probe 的
设计边界——它验证的是"连通性 + 方言"，不是"这条长对话能不能过"。混淆
两者会得出"probe 说健康，为什么还被摘"的错误期待；2026-09-17 起这类 400
已经不再触发摘牌（见 §1），但 probe 本身仍然测不到它。

## 3. 熔断表：谁被摘了、为什么

```bash
newgate breaker      # 只列当前被摘牌的 binding：多久了、连续失败几次、上次探活结论
newgate probe        # 重新探活；健康的候选会在冷却期（60s）后自动解封
newgate tier <档位>   # 这个 binding 在链里排第几、是不是被 maxAttempts 截断
```

熔断解封**只能靠 probe 证明**：正常流量成功（`RecordSuccess`）故意不解封，
这是设计——熔断状态必须经过独立的健康探测确认，不能让"客户端凭空又发对
了一次"就当作恢复。

## 4. 接管后仍然直连

```bash
newgate status
newgate doctor
newgate shim status
```

检查 PATH 顺序、真实 executable、shell 中旧 base URL 环境变量，以及 OpenCode
配置是否被另一个工具覆盖。PATH 改动通常需要新 shell。

## 5. Restart 后行为没变


`newgate version` 显示 CLI 文件版本，不证明 daemon 已换血。读取 pidfile 后比较：

```bash
md5sum /proc/<pid>/exe /path/to/newgate
```

不一致说明升级没有接管成功。

## 6. 权限错误

Daemon 用户必须能写 state、pid、lock、log 和 thinkcache 文件。目录建议保留
group 继承位，文件需要 daemon 所在组可写。优雅交接 500 或冷层关闭通常是同一类
权限问题。

## 7. Fallback 不符合预期

用 `newgate tier <role>` 查看 resolver 输出，用 `newgate metrics` 看
`chain.step_failed` 与 `chain.failover`，再从响应头确认最终 route。不要只看
profile 文本猜链。

## 8. 请求被改坏

以 dump 三联文件为唯一报文证据。对比 client-sent 和 we-sent，找到具体 splice；
再用日志 notes 确认是 schema repair、special treatment 还是 model rewrite。
