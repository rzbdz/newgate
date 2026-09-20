# 测试与安全

## 1. 默认不出网

Go 单测使用 `httptest` 或本地 listener。任何需要真实 provider token 的验证必须
显式运行，不能混入默认测试。

## 1.1 四层粒度

改动落在哪一层，就该跑哪一层的测试。四层各有各的盲区，谁都替不了谁：

| 层 | 在哪 | 装什么 | 覆盖什么 | 盲区 |
| --- | --- | --- | --- | --- |
| 单元 | 各包 `*_test.go` | 纯函数 / 单个 struct | 算法、解析、边界 | 接线（谁注册了什么） |
| 模块 | `go/testing/testkit` | 我 + 我的依赖链，真组件图 | 接口通过性、注册/撤销对称性、模块自己的特殊行为 | 跨模块的真实数据面 |
| 系统 | `go/testing/system` | 整张真组件图 + 真转发服务 + 假上游 | 路由、fallback、thinkcache、逐字节、流式分帧 | 真进程、真接管、真配置改写 |
| 端到端 | `mock/*.sh` | 真二进制、真进程、真接管 | 「装出来的东西能不能跑」 | 报文级细节（断言靠 grep） |

分工的原则是**测试跟着「谁知道这件事」走**：

- 基础、通用、不常改的功能（档位解析、字节手术、转移、流式）做**大测试**——
  它们一改就全线出问题，值得在系统层被反复覆盖。
- 各模块自己的特殊功能（omo 槽位的动态键、某个上游补丁的判据、接管时写的
  哪些字段）**只能放在模块内部**：只有模块自己清楚它要什么、边界在哪、怎么
  动态更新。拿系统层去测模块内部细节，会让系统层长出一堆和实现绑死的断言，
  模块一动就得改测试——那是测试在拖后腿，不是保护。

`mock/` 下那两条是真端到端，而且刻意与 Go 侧解耦：它们跑的是**装出来的二进
制**，不 import 任何 core 包。所以 Go 侧怎么重构都不该影响它们——它们一红，
说明行为真的变了。

### 1.2 两个仓库各测各的（2026-09-20 起）

发行版的模块不在本仓库（见 `docs/09-extension-guide.md` §8），所以测试分成两段，
**发行版流水线里的第一项就是内核的全部测试**：

| 段 | 在哪跑 | 覆盖 |
| --- | --- | --- |
| `core-test` | 内核仓库（`cd go && GOPROXY=off go test ./...`） | 内核的逻辑 + 内核的模块 |
| `dist-test` | 发行版仓库（`cd go && go test ./...`） | 发行版自己的模块 |
| 端到端 | 发行版仓库（`mock/e2e_claude_dist.sh`） | 发行版模块的真实行为（真二进制 + 内核的假上游） |

2026-09-20 之前不是这样：内核的 `app/default_test.go`、`testing/system/`、
`modules/gateway/forward/` 里都躺着断言**发行版模块**行为的用例，内核的 e2e 里还有
整整五章在验 DeepSeek 的补丁。后果是内核摘掉那个模块就红——**内核再也测不干净**。
那些断言跟着拥有者搬走了；内核留下的是**机制**的覆盖（例如
`testing/system/shape_test.go` 用测试自己的合成判据，断言「形状 400 → 认领 → 只计数
不摘牌 → 日志留痕」这条因果链的每一环都是内核自己的）。

由此有一条约束要记住：**`go/testing/{testkit,system,upstream}` 从这时起是对外 API**
——发行版拿 `system.StartWith(t, 自己的 loader)` 起真图测自己的模块。重塑这几个包的
形状会是一次跨仓库的破坏性变更，而症状出现在别人的 CI 上。

**模块层**用 `testkit` 起图，只传你关心的组件，依赖由 capability 推导：

```go
graph := testkit.Start(t,
    modules.Component{Name: "stub-runtime", Provides: ...},
    wrapperapi.New(),            // 被测模块是真的
)
graph.Before("stub-runtime", "wrapper")
w := testkit.Get(graph, wrapperapi.Capability)
```

`testkit.Sandbox(t)` 把 `NEWGATE_HOME` / `NEWGATE_TARGET_DIR` / `HOME` 圈进临时
目录并清掉会从父会话漏进来的变量（`NEWGATE_DEPTH`、`CLAUDE_CODE_MAX_CONTEXT_TOKENS`
之类）——不 clean 的话，在一个被接管的会话里跑测试会得到假失败。

**系统层**用 `system.Start(t)`：它向内核要一个临时端口（**不是 8899**），
所以能在开发机上一边用着线上 newgate、一边跑系统测试，互不干扰。这也是
「换版本之前先在别的端口跑通」的落地方式。它失败时会把代理自己那几句日志倒
到测试输出里（`无可用候选。跳过原因：…` 是典型）——路由类断言红掉时，那几
行才是证据。

系统层用的假上游是 `go/testing/upstream`：`mock/fake_upstream.py` 的进程内替身，
两种方言 + 严格 DeepSeek 口径 + `FailNext`/`Requests`/`SetChunkDelay`。它的
`Client()` **不走环境代理**——跑测试的会话自己就穿行在 newgate 里，环境里挂着
代理时发给假上游的请求会先被真网关转一手，断言就全乱了。

还有一条不变量测试值得单独知道：`app.TestGraphCanBeRebuiltAfterStop` 在同一个
进程里装两次图。模块的 Start 往全局注册东西（插件、Agent 描述符、角色提供者），
Stop 漏一个 Release 的症状是**只有第二次装配才炸**——单跑任何一条测试都是绿的。

## 1.2 标准验证

```bash
cd go
make check          # 一把梭：格式 + vet + 生成清单 + 单测 + 两条零 token 端到端
```

拆开来跑：

```bash
make check-fmt          # 只校验格式
make vet
make check-generate     # 装配清单是否过期
make test               # go test ./...
make test-race          # 并发相关改动必跑
make e2e                # opencode 侧零 token 端到端
make e2e-claude         # Claude Code 侧零 token 端到端
make static             # 静态二进制（**内核自己的**；产品二进制从发行版仓库出）
```

零 token E2E 也可以直接跑脚本，端口和沙箱可用环境变量岔开（人肉并行）：

```bash
cd ..
bash mock/e2e.sh
bash mock/e2e_claude.sh
# 并行：NEWGATE_E2E_UP_PORT / NEWGATE_E2E_PROXY_PORT / NEWGATE_E2E_SANDBOX
```

OpenCode E2E 覆盖接管、role 路由、热切换、stream 和逐字节恢复。Claude E2E
覆盖 profile 注入、严格 DeepSeek、thinkcache、count_tokens、优雅交接和 metrics。

## 1.3 要花真 token 的那条

```bash
bash mock/e2e_reasoning_affinity.sh
```

它**故意不在 `go test ./...` 里，也不在 CI 里**——打真上游，会花钱。默认连
`http://127.0.0.1:8899`（本机跑着的 daemon），可用 `NEWGATE_URL` /
`SOURCE_PROFILE` / `MODEL` / `EXPECTED_TARGET` 覆盖。改路由或 reasoning
亲和性相关逻辑时人工跑一次。

## 1.4 CI

`.github/workflows/ci.yml` 三个 job：`static`（格式/vet/生成清单/单测）、
`race`、`e2e`（两条零 token 端到端）。真 token 那条不在里面。

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
