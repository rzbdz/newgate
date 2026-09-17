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

**这条根因已修**（2026-09-17）：`repairTailShape`（modules/deepseek）在思考
模式下检测「最后一条 user 消息的 content 是数组、且一个 text 块都没有」的
尾部，追一句用户口气的继续指令（`Continue from the tool results
above…`），让 DeepSeek 的校验落在一条真正的指令上。只认最后一条、只碰这种
形状；已带文字的尾部一律不碰，避免给正常对话加噪音。证据留样：`dump/`
里 `err-400-req000412/req000464` 两份 958KB 真实对话，修复后不再误报
reasoning_content 缺失。新出现的 400 仍按 §3 分类器处理（形状错误永不摘牌、
只计数），但根因从「不修的已知问题」降级为「只剩没撞上的个案」。

**被形状判据认领的 400 会额外存一份专属证据**（2026-09-17 起）：
`dump/shape-400-<判据名>/req-<id>-<纳秒>/`，里面是 client-sent / we-sent /
upstream-said / audit.txt / meta.txt 五件。目录名里的判据名就是日志那行
`判据 X` 里的 X（名字会过一遍白名单，非字母数字一律换成 `_`），所以看到
`dump/shape-400-deepseek/` 就知道是 DeepSeek 那条 reasoning 回传校验认的。
这份证据**不参与 `req-*` / `err-*` 那套按条数滚动清理**，只按总字节封顶
（512MB，超了从最旧的子目录开始删），因为它是「判据为什么这么判」的唯一
现场；`dump/err-400-*` 记的是「上游回了什么」，两者常常同时出现、看的角度
不同。

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
newgate breaker      # 两段：被摘牌的（状态/账本/还要等多久/原因）+ 只计数没摘牌的
newgate probe        # 不等冷却，立刻用一次主动探活改结论
newgate tier <档位>   # 这个 binding 在链里排第几、是不是被 maxAttempts 截断
```

熔断的恢复是**半开**的：冷却期满后放行一次真实请求作试探，成功即合闸，失败
则回闸并把冷却翻倍（60s → 120s → … → 10 分钟封顶）。所以「摘了只能等 probe」
已经是过去式——`newgate probe` 现在的作用是**立刻**改结论，以及处理那些真实
流量触发不到的问题（典型例子见 §1：probe 发的最小请求不带历史，探不到
reasoning 校验，反过来也探不到依赖长上下文的故障）。

半开的代价是坏上游会周期性被放回来撞一次用户请求，所以设计上刻意收紧了三点：
只在冷却期满放行、半开期严格一次（并发请求仍被挡在外面）、试探失败即退避 ×2。
`newgate breaker` 的「还要等」列显示的就是退避之后的实际冷却，对不上 60s 基准
是正常的。

看到「明明能用却被摘了」，先看账本那一列：

- **可用性**：连接超时/5xx/流中断，真的不行；
- **限流**：429，20s 起就回来；
- **配置**：401/403/404，一次即摘，去查 key / base / 模型名；
- **请求形状**：永不摘牌，只累加计数——它出现在第二段里，说明有 400 被挡住了
  但上游没被摘（见 §1）。

`newgate metrics` 的 `breaker.skipped.shape_error` 是同一件事的计数器。

**「明明能用却被摘了」还有第三种可能：诊断探活也没救回来。** 连续失败到阈值时
会先做一次主动探活，探活说还通就不摘（记进 `spared`）。所以如果一条 binding
确实被摘了，说明**被动流量和主动探活都说不通**——这时候去看原因那一列：

- 只写「真实流量连续失败」→ 探活也跑了但结论是不通（或没有 verifier）；
- 写「诊断探活也不通」→ 双重证据，摘得没冤枉它；
- 「半开试探失败」→ 它在冷却期满后拿到过一次真实试探名额，那一发也失败了。

`newgate breaker` 第二段的「救回」列是相反方向的记录：这条 binding 一共被
诊断探活从摘牌边缘拉回来过几次（跨重启累计）。这个数字在涨而 `可用性` 账本
一直是 0，说明它间歇性抖动但每次都被探活证明还活着——不是故障，是上游在喘。

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
