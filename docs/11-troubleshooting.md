# 故障排查

## 1. reasoning_content 400

先看日志是否出现真实回填、占位符或 tool-loop rebase notes。再检查
`dump/err-400-*` 的 client-sent、we-sent、upstream-said：

- 客户端没带，但 we-sent 有真实值：cache 命中；
- we-sent 是占位符：cache 未命中；
- we-sent 缺字段：插件未匹配或被关闭；
- 上游仍拒绝：检查方言和 model 是否属于严格 thinking mode。

## 2. Probe 绿但请求 404

Provider 可能只配置了声明协议入口。分别测试 Anthropic `/v1/messages` 和
OpenAI `/v1/chat/completions`，检查 `anthropic_url` 与通用 base URL。

## 3. 接管后仍然直连

```bash
newgate status
newgate doctor
newgate shim status
```

检查 PATH 顺序、真实 executable、shell 中旧 base URL 环境变量，以及 OpenCode
配置是否被另一个工具覆盖。PATH 改动通常需要新 shell。

## 4. Restart 后行为没变

`newgate version` 显示 CLI 文件版本，不证明 daemon 已换血。读取 pidfile 后比较：

```bash
md5sum /proc/<pid>/exe /path/to/newgate
```

不一致说明升级没有接管成功。

## 5. 权限错误

Daemon 用户必须能写 state、pid、lock、log 和 thinkcache 文件。目录建议保留
group 继承位，文件需要 daemon 所在组可写。优雅交接 500 或冷层关闭通常是同一类
权限问题。

## 6. Fallback 不符合预期

用 `newgate tier <role>` 查看 resolver 输出，用 `newgate metrics` 看
`chain.step_failed` 与 `chain.failover`，再从响应头确认最终 route。不要只看
profile 文本猜链。

## 7. 请求被改坏

以 dump 三联文件为唯一报文证据。对比 client-sent 和 we-sent，找到具体 splice；
再用日志 notes 确认是 schema repair、special treatment 还是 model rewrite。
