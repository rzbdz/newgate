# 测试与安全

## 1. 默认不出网

Go 单测使用 `httptest` 或本地 listener。任何需要真实 provider token 的验证必须
显式运行，不能混入默认测试。

标准验证：

```bash
cd go
go test ./...
go vet ./...
go test -race ./...
make static
```

零 token E2E：

```bash
cd ..
bash mock/e2e.sh
bash mock/e2e_claude.sh
```

OpenCode E2E 覆盖接管、role 路由、热切换、stream 和逐字节恢复。Claude E2E
覆盖 profile 注入、严格 DeepSeek、thinkcache、count_tokens、优雅交接和 metrics。

## 2. Build tags

每个 `build-*` tag 必须独立 checkout 后通过 `go test ./...`。Tag 之间允许临时
不编译，用于逐概念 review。

## 3. 凭证

- 日志和诊断输出必须脱敏；
- 控制端点使用 state 中的 bearer token；
- dump 可能包含请求正文，只能保存在用户配置目录；
- 不能把真实 token、现场 dump 或用户配置提交进 Git。

## 4. 本地网络

Daemon 默认监听 loopback。探活请求显式绕过环境代理，避免 HTTP_PROXY 劫持
127.0.0.1。跨用户控制必须通过 token，不依赖文件所有者猜测授权。

## 5. 请求安全

请求改写使用 byte-level 操作，避免 JSON round-trip 改变未知字段或大整数。
插件异常 fail-open，但必须记录错误；不能吞掉失败并返回成功形状的假响应。

## 6. 并发

Watcher 快照、registry、health、metrics 和 thinkcache 的共享状态必须通过锁或
原子值保护。涉及 goroutine 的测试必须通过 race detector。
