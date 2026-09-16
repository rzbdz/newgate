# CLI 与运维

## 1. 生命周期

```bash
newgate init
newgate start
newgate status
newgate restart
newgate stop
```

`start` 启动 daemon 并接管未显式关闭的 Agent。`stop` 释放接管并停止 daemon。
`restart` 优先走 listener fd 交接：旧进程排空在途请求，新进程接收新连接。

单独控制 Agent：

```bash
newgate on claude
newgate off claude
newgate on opencode
```

## 2. Profile 与路由

```bash
newgate profiles
newgate tier normal
newgate probe
newgate probe ds
```

`tier` 展示解析后的完整候选链和跳过原因。`probe` 主动验证 provider 连接、方言和
已知怪癖；它不能替代真实请求路径的双协议验证。

一次调用覆盖：

```bash
newgate claude --profile=ds
```

该覆盖编码到 Gateway URL，不修改全局默认。

## 3. 观测

```bash
newgate metrics
newgate st
newgate logs
newgate alllogs
newgate doctor
```

`doctor` 检查配置、provider、端口、接管和模块诊断。`st` 列出请求插件及存在原因。
`alllogs` 汇总版本、环境、状态和脱敏配置。

## 4. 部署

本机构建：

```bash
cd go
make build
```

跨机器使用：

```bash
make static
```

替换运行中二进制应先写新文件再 rename，避免 `Text file busy`。升级后比较
`/proc/<pid>/exe` 与目标二进制摘要，确认 daemon 已换成新版本。

## 5. 沙箱

设置 `NEWGATE_HOME` 可创建完全独立的配置和 daemon 状态。E2E 脚本依赖该机制，
不会改动真实用户配置。
