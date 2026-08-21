# 17 · 测试框架（M0）

> **第一件要写的代码。** 不是因为纪律，是因为这套 mock 同时是[发现探针](10-llm-native.md)的实现——测试基建和产品功能是同一份代码。

## 1. 铁律

| 规则 | 理由 |
| --- | --- |
| **测试不消耗任何 token** | 永远不调真实 LLM。一次都不行 |
| **测试不出网** | CI 里阻断 egress；试图连外网的测试**判定为失败**，不是跳过 |
| **测试不需要安装真实 CLI** | 用假 tool，CI 里装 8 个 AI CLI 不现实也不稳定 |
| **测试不碰真实 `$HOME`** | 全部在临时 `NEWGATE_HOME` 里跑 |
| **确定性** | 不用真实时钟、不用真实随机、端口由框架分配后注入 |

「出网即失败」这条要在 CI 层强制（网络命名空间/防火墙），不能只靠自觉。**一个偷偷连了真实 API 的测试，会在某天变成 CI 里的一笔账单和一个 flaky。**

## 2. 三个假件

```
┌────────────┐    ┌──────────┐    ┌───────────────┐
│  FakeTool  │───▶│ newgate  │───▶│ FakeUpstream  │
│ 假的 AI CLI │    │ (被测)    │    │ 假的 LLM API  │
└────────────┘    └──────────┘    └───────────────┘
      ▲                                    │
      └──────── MockFactory 装配 ───────────┘
```

### 2.1 FakeUpstream — 假的 LLM API ★ 核心

一个本地 HTTP server，**同时挂载 anthropic / openai / gemini 三套路由**，记录收到的一切，按脚本返回。

```ts
interface FakeUpstream {
  url: string;                          // http://127.0.0.1:<ephemeral>
  requests: RecordedRequest[];          // 收到的全部请求：path/method/headers/body/时间

  // 脚本化响应
  reply(spec: ReplySpec): void;
  replyOnce(spec: ReplySpec): void;

  close(): void;
}

interface RecordedRequest {
  path: string;                         // '/v1/messages' → 用来实测协议方言
  headers: Record<string, string>;      // 用来验证凭证注入的变量名对不对
  body: unknown;                        // 用来验证 model 字段被正确改写
  receivedAt: number;                   // 逻辑时钟
}
```

**必须能模拟的失败**（这些是产品要处理的真实情况，不是边角料）：

| 场景 | 用途 |
| --- | --- |
| `401` / `403` | 凭证注入错、变量名错 |
| `404` | baseUrl 的 `/v1` 后缀问题（最高频故障） |
| `429` + `Retry-After` | 重试与熔断 |
| `500` / `502` / `529` | 故障转移 |
| **首字节延迟 N 秒** | 首字超时（60s）测试 |
| **流到一半静默 N 秒** | 静默超时（120s）测试 |
| **流到一半断连** | 「已开始输出就不能重试」这条红线 |
| **畸形 SSE** | 解析健壮性 |
| **慢速逐块吐** | 验证代理**没有缓冲**整个响应 |
| **连接直接 reset** | fail-open 行为 |

超时测试**不能真的睡 60 秒**。框架注入一个可控时钟，FakeUpstream 和被测代码共用它。

### 2.2 FakeTool — 假的 AI CLI

一个几十行的小程序，行为**由声明式配置决定**，用来穷举真实 CLI 的各种脾气：

```ts
interface FakeToolSpec {
  // 它从哪里读 baseUrl / key？
  reads: 'env' | 'config' | 'config-beats-env' | 'env-beats-config' | 'neither';
  envNames?: { baseUrl: string; key: string; model?: string };
  configPath?: string;
  configDirEnv?: string;               // 支不支持 CLAUDE_CONFIG_DIR 那种

  // 特殊脾气
  ignoresProxyEnv?: boolean;           // 模拟 Node 内置 fetch 不读 HTTPS_PROXY
  pinsCertificate?: boolean;           // 模拟证书固定 → L2 必须给出正确诊断
  rewritesOwnConfig?: boolean;         // 模拟工具自己覆写配置（漂移）
  modelSlots?: string[];               // 多模型槽位，测 role 枚举与 preset

  behavior: 'one-request' | 'stream' | 'echo-env' | 'exit-with-code';
}
```

它做的事就是：按 `reads` 策略取到 baseUrl/key/model → 发一个请求 → 把收到的东西打到 stdout → 退出。

**为什么这个比用真实 CLI 更有价值**：

- 真实 CLI 只能测「它现在的行为」；FakeTool 能测「**所有**可能的行为」，包括还没遇到的。
- `ignoresProxyEnv` / `pinsCertificate` 这些失败模式，在真实 CLI 上很难稳定复现，但产品必须正确诊断它们（[11-proxy-native.md](11-proxy-native.md) §3.6）。
- CI 里零依赖、毫秒级、不会因为上游发版而 flaky。

### 2.3 MockFactory — 声明式装配

```ts
const env = await mock({
  upstreams: {
    cheap: { protocol: 'anthropic', latencyMs: 10 },
    dead:  { protocol: 'anthropic', alwaysFail: 503 },
  },
  tools: {
    faketool: { reads: 'env', envNames: {...}, modelSlots: ['heavy','cheap'] },
  },
  bindings: { heavy: 'cheap/model-a', cheap: 'cheap/model-b' },
});

await env.run('faketool');

expect(env.upstream('cheap').requests[0].body.model).toBe('model-a');
await env.teardown();          // 断言：临时目录清空、端口释放、无残留进程
```

`teardown()` 自己也要断言干净，否则测试之间互相污染，排查起来极痛苦。

## 3. 测试层次

| 层 | 测什么 | 需要什么 | 速度 |
| --- | --- | --- | --- |
| **L0 纯函数** | argv 切分、preset 解析（`*` 通配 / `extends`）、role→模型解析、模型名归一化 | 无 | µs |
| **L1 存储** | 原子写、并发写、schema 校验、迁移 | 临时目录 | ms |
| **L2 代理** | 改写 `model` 字段、透传、流式逐块、三段超时、fail-open、路径解析 `/p/<preset>` | FakeUpstream | ms |
| **L3 bootstrap** | 配置打补丁、diff 展示、`newgate off` **逐字节还原** | 临时 HOME | ms |
| **L4 端到端** | FakeTool → newgate → FakeUpstream 全链路 | 三件齐全 | 10ms |
| **L5 描述符契约** | **每个 tool 描述符跑同一套用例** | FakeUpstream | ms |
| **L6 真实工具** | 真 CLI + FakeUpstream（**仍不出网**） | 装了真 CLI | s |
| ~~L7 真实 API~~ | — | **不做**，见 §6 | — |

L0–L5 是 CI 必跑，总时长目标 **< 10 秒**。L6 单独 job，可选。

## 4. L5 描述符契约测试 ★ 这条决定项目能不能长期活

「接一个新工具 = 写一份 JSON」这个承诺，靠的就是**每份描述符都能自动跑过同一套契约**：

```
给定 descriptor.json：
  1. schema 校验通过
  2. plan() 是纯函数（同输入两次调用结果相同、无副作用）
  3. bootstrap 到临时 HOME → 配置文件被正确打补丁 → 快照对比
  4. FakeTool 按该描述符的 reads 策略启动 → FakeUpstream 收到请求
  5. 请求里的凭证在描述符声明的位置（header 名对不对）
  6. 请求体的 model 是我们注入的语义名
  7. `newgate off` → 配置文件与 bootstrap 前**逐字节相同**（含注释与缩进）
  8. 声明的每个 role 都能被独立绑定并生效
```

新增一个工具的 PR，只要 JSON 进了目录，这 8 条自动跑。**跑不过就不合并。** 这样描述符质量不依赖 review 的仔细程度。

## 5. Golden 测试（流式）

流式是 M1 唯一需要下重手的地方（[16-core-thesis.md](16-core-thesis.md) §6.5），也是最容易悄悄坏掉的地方。

做法：录制一组**逐块的** SSE 报文存成 golden 文件，断言代理输出的**分块边界和时序**与输入一致——不只是拼起来内容相同。

```
tests/golden/
  anthropic-simple.sse
  anthropic-tool-use.sse           # 工具调用，AI CLI 最依赖
  anthropic-parallel-tools.sse
  anthropic-thinking.sse
  openai-chat-stream.sse
  malformed-truncated.sse
```

**「拼起来内容相同」是不够的**：如果代理缓冲了整个响应再一次性吐出，内容测试会通过，但用户体验完全毁掉（看起来像卡死）。所以断言必须包含分块结构。

## 6. 明确不做的测试

| 不做 | 为什么 |
| --- | --- |
| 调真实 LLM API | 花钱、不稳定、结果不确定、CI 里跑不了 |
| 断言模型输出质量 | 不是我们的职责——我们搬运请求，不管内容 |
| 真实中转服务的兼容性测试 | 它们会变，测了也不代表明天还对。改用 `newgate doctor --probe` 让用户自己实测 |

## 7. 与发现探针的统一 ★

[10-llm-native.md](10-llm-native.md) §2 阶段 D 的探针需要的东西，和 FakeUpstream **完全一致**：起一个假上游、让工具真的去连、看它带了什么。

所以：

```
FakeUpstream ──┬─▶ 测试里当 fixture
               └─▶ 生产里当 `newgate doctor --probe <tool>` 的裁判
```

**一份代码，两个用途。** 这带来三个好处：

1. 先写测试框架不是"开销"，是直接在写产品功能。
2. 探针是产品里被测试覆盖最密的组件（它自己就是测试基建）。
3. LLM 发现出来的描述符，验证路径和 CI 里跑的契约测试是同一条——线上线下行为一致。

## 8. 目录

```
tests/
  mock/
    upstream.*          # FakeUpstream（同时被 src/probe 引用）
    tool.*              # FakeTool
    factory.*           # MockFactory
    clock.*             # 可控时钟
  golden/               # SSE 逐块报文
  unit/                 # L0
  store/                # L1
  proxy/                # L2
  bootstrap/            # L3
  e2e/                  # L4
  descriptors/          # L5 契约（对 tools/*.json 参数化）
  live/                 # L6，默认跳过
src/
  probe/                # 生产探针，复用 tests/mock/upstream
```

`tests/mock/upstream` 被生产代码引用，所以它**不能**放在只有测试才编译的位置——它是产品的一部分，按产品代码的标准写和 review。

## 9. M0 完成标准

- [ ] FakeUpstream：三协议路由 + 请求记录 + §2.1 全部失败场景 + 可控时钟
- [ ] FakeTool：`reads` 五种策略 + `ignoresProxyEnv` / `pinsCertificate` / `rewritesOwnConfig`
- [ ] MockFactory：声明式装配 + `teardown()` 自断言
- [ ] CI：egress 阻断，出网即失败
- [ ] 一条贯通的 L4 用例（FakeTool → newgate stub → FakeUpstream）作为骨架
- [ ] L0 的 argv 切分器测试全部写好（[03-cli-spec.md](03-cli-spec.md) §2 逐例验证表，一行一个用例）——**实现可以先不写，测试先在**

最后一条是有意的：argv 切分规则密、歧义多，**先把那张表变成红色的测试，再去写实现**，能逼出设计里没想清楚的地方。
