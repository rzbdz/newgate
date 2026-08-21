# 11 · 原生代理（虚拟模型命名空间 + 全代理拦截）

> 需求 (4)：proxy 启用时，tool 的配置文件直接写成内部名（如 `newgate/opencode.search`），由代理做转换；并进一步支持**全代理模式**——不改配置文件，注入 `HTTPS_PROXY`/WebSocket 代理，靠模糊匹配劫持它原有的 endpoint 与模型名。

[08-gateway.md](08-gateway.md) 讲的是网关本身（路由、翻译、观测）。这一篇讲的是**它如何接管 tool**，也就是拦截层。

## 1. 四个接管等级

从「改得多、靠谱」到「改得少、通用」：

| Level | 名称 | 改动 tool 配置 | 需要 MITM | 能力 | 适用 |
| --- | --- | --- | --- | --- | --- |
| **L0** | baseUrl 重定向 | 只改 baseUrl/key | 否 | 换 provider | 单模型工具，最稳 |
| **L1** | 虚拟模型命名空间 | 改一次，永不再改 | 否 | 每个 role 独立热切换 | **多模型工具的主力方案** |
| **L2** | 透明拦截（全代理） | **完全不改** | 是 | 零配置接管任何工具 | 未适配的工具、临时接管 |
| **L3** | 进程内劫持 | 完全不改 | 否 | 同 L2 但无需证书 | Node 系 CLI |
| ~~L4~~ | tun/eBPF 系统级 | — | — | — | **明确不做**（见 §7） |

**默认走 L1**；L1 不适用（工具拒绝未知模型名）时降级到 L2/L3；L2/L3 是「野路子兜底」，默认关闭，需显式开启。

## 2. L1 · 虚拟模型命名空间

### 2.1 核心想法

配置文件里的模型名，从**具体模型**换成**角色引用**。

```jsonc
// 接管前：改一次 provider 就要改 10 个地方
{
  "model": "anthropic/claude-opus-4",
  "small_model": "anthropic/claude-haiku-4-5",
  "agent": {
    "search": { "model": "openai/gpt-4o-mini" },
    "think":  { "model": "deepseek/deepseek-reasoner" }
  }
}

// 接管后：配置文件永远长这样，再也不用动
{
  "provider": {
    "newgate": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8788/v1/openai" },
      "models": {
        "opencode.primary": {}, "opencode.fast": {},
        "opencode.search": {},  "opencode.think": {}
      }
    }
  },
  "model": "newgate/opencode.primary",
  "small_model": "newgate/opencode.fast",
  "agent": {
    "search": { "model": "newgate/opencode.search" },
    "think":  { "model": "newgate/opencode.think" }
  }
}
```

> 上面 opencode 的字段名是**示意结构**，实际字段以发现流水线的探针结果为准（[05-tools.md](05-tools.md) 核实清单）。

代理收到 `model: "opencode.search"` → 查当前 Preset 的绑定 → 得到 `(provider=local-vllm, model=qwen3-32b)` → 改写请求体 → 路由到真实上游。

### 2.2 为什么这是质变

| | L0（改 baseUrl） | L1（虚拟模型名） |
| --- | --- | --- |
| 换 provider | 改配置 + 重启 tool | **控制面点一下，正在跑的会话立刻生效** |
| 10 个模型槽位 | 改 10 处，且必须同 provider | 每个 role 独立绑定，可以跨 provider 混搭 |
| 配置文件 | 每次切换都被改写 | **写入一次，此后只读** |
| 并发多 session 用不同配置 | 不可能（共享同一份配置文件） | 天然支持（per-session 令牌带路由上下文） |

「配置文件写入一次，此后只读」是关键性质：它把「切换」从**文件系统操作**变成了**控制面操作**。所有围绕文件改写产生的问题——备份、回滚、冲突、并发、格式丢失、tool 自己写回——**全部消失**。

### 2.3 命名格式

```
<provider-slot>/<tool>.<role>
        │           │      └── 角色 id
        │           └───────── tool id（同一个网关服务多个工具，需要隔离命名空间）
        └───────────────────── 在 tool 配置里注册的虚拟 provider 名，固定为 'newgate'
```

用 `.` 而不是第二个 `/` 分隔——很多工具用 `provider/model` 单斜杠解析模型名，多一个斜杠会解析错。

### 2.4 风险与降级

| 风险 | 处理 |
| --- | --- |
| 工具用白名单校验模型名，拒绝未知名 | 优先把虚拟模型注册成工具支持的「自定义 provider」条目（多数工具都有这个口子）；不行则降级 L2 |
| 工具按模型名推断能力（是否支持视觉/工具调用/上下文长度） | 虚拟模型名里编码能力提示，或在注册条目里声明 capabilities；发现阶段要探测这一点 |
| 工具按模型名计费/计 token | 代理在响应里回填真实模型名与用量（`x-newgate-actual-model`） |
| 用户手工改了配置文件 | 注入前比对指纹，发现被改动则提示重新写入 |

**这些风险必须靠探针实测确认，不能靠猜**——发现流水线的阶段 D 正是为此存在（[10-llm-native.md](10-llm-native.md) §2）。

## 3. L2 · 透明拦截（全代理模式）

**一行配置都不改**，直接接管一个从没适配过的工具。

### 3.1 机制

```
1. 生成/复用 newgate 的本地 CA（每次安装一份，私钥 0600）
2. 起代理，监听 127.0.0.1:8789（HTTP CONNECT + HTTP 正向代理）
3. spawn tool 时注入：
     HTTPS_PROXY / HTTP_PROXY / ALL_PROXY = http://127.0.0.1:8789
     NO_PROXY = localhost,127.0.0.1,::1,<用户已有的>
     NODE_EXTRA_CA_CERTS / SSL_CERT_FILE / REQUESTS_CA_BUNDLE /
     CURL_CA_BUNDLE / GIT_SSL_CAINFO / DENO_CERT = <newgate CA 路径>
4. tool 发请求 → CONNECT api.anthropic.com:443 → 我们用 CA 现签证书完成 TLS
5. 按「劫持名单」判定：命中 → 改写并转发到选定 provider；未命中 → 原样透传
```

### 3.2 关键安全性质：作用域受限的信任

> **CA 绝不安装进系统信任库。**

只有 newgate **自己 spawn 的子进程**才通过环境变量信任这个 CA。你的浏览器、你的 git、你的其他终端一概不受影响。

这是与「装个根证书然后全局 MITM」的本质区别：攻击面被限制在单个进程的生命周期内。CA 私钥丢了，影响范围也仅限于「能骗过被 newgate 拉起的进程」。

配套要求：

- CA 私钥 0600，放 `state/` 不放 `config/`（不进 dotfiles 同步）。
- 每台机器独立生成，**绝不随安装包分发**（分发的 CA = 人尽皆知的私钥 = 灾难）。
- `newgate ca show / regenerate / trust-check`，UI 上显著展示当前 CA 指纹。
- 代理只监听 loopback，且要求 per-session 令牌（否则本机任意进程都能借道）。

### 3.3 劫持名单从哪来

**不要硬编码域名列表，也不要劫持一切。** 从发现阶段的产物构造：

```
劫持名单 = 配置文件里正则抓到的 https?://… 主机
         ∪ 二进制/JS 里 strings 抓到的形似 API endpoint 的字面量
         ∪ 内置的已知 LLM API 域名表
         − 用户黑名单
```

**其余流量一律原样透传**（npm registry、GitHub、遥测、更新检查）。理由：

1. 少碰不该碰的东西，减小故障面和信任面。
2. 证书固定（cert pinning）的服务被我们 MITM 会直接失败，透传就没事。
3. 用户能审计「newgate 到底看了我哪些流量」——这个清单必须在 UI 上可见。

### 3.4 模型名模糊匹配

L2 下配置文件没改，请求体里是它原本的模型名（`claude-opus-4`）。要把它映射到 role：

```
1. 精确别名表（内置 + 用户自定义 + 上次确认过的）     ← 优先，确定性
2. 归一化匹配：去 provider 前缀、去日期后缀、小写、去连字符
3. 家族/档位启发式：
     opus|gpt-5|r1|pro           → 大档  → 绑到 primary/reasoning
     sonnet|gpt-4o|v3            → 中档  → primary
     haiku|mini|flash|lite|small → 小档  → fast
     含 thinking|reasoner|-r1|o1 → reasoning
     含 embed                    → embed
4. 都不中 → 不猜
```

**第 4 条是红线。** 静默错配比失败更糟：用 `fast` 的绑定跑本该 `reasoning` 的任务，结果是质量莫名其妙地烂，而用户完全不知道发生了什么；反过来则是账单爆炸。

未命中时的默认行为（可配置）：

| 策略 | 行为 |
| --- | --- |
| `passthrough`（**默认**） | 原样转发到它本来要去的地方，只记录一条「见到未知模型 X」 |
| `ask` | 阻塞并在 TUI/Web 上弹一次确认，确认结果写入别名表，以后不再问 |
| `fallback` | 用兜底绑定，**每次都在 stderr 警告** |
| `error` | 直接失败 |

所有模糊命中（第 2、3 条规则）都必须**记日志**，并在会话结束时汇总提示：「本次有 3 个请求走了模糊匹配：claude-opus-4 → primary(kimi-k2)」。用户看到一次，就知道该不该去建个精确别名。

### 3.5 WebSocket

`wss://` 走 CONNECT 隧道，MITM 建立后与 HTTPS 同路径处理；`ws://` 走普通 HTTP 升级。需要额外处理的是**帧级转发不能缓冲**，以及子协议协商要原样传递。有些工具用 WS 做流式，漏了这块就表现为「一直转圈」。

### 3.6 已知失败模式（必须能诊断出来，而不是卡住）

| 现象 | 原因 | 提示 |
| --- | --- | --- |
| TLS 握手失败 | 客户端做了证书固定 | 「该工具固定了证书，无法透明拦截，请改用 L0/L1」 |
| 完全没有流量进代理 | 运行时不读 proxy 环境变量（见 §4） | 「检测到 Node 运行时，建议改用 L3 进程内劫持」 |
| 部分流量绕过 | 用了自带的 HTTP 实现或 QUIC/HTTP3 | 列出绕过的目标，建议加 L0 |
| 公司网络本来就有代理 | 需要级联 | 支持 `upstreamProxy` 配置，把原有代理作为我们的出口 |

## 4. L3 · 进程内劫持（Node 系）

L2 对 Node CLI 有个现实障碍：**Node 内置的 `fetch`（undici）默认不读 `HTTP_PROXY`/`HTTPS_PROXY` 环境变量**，除非应用自己装了 ProxyAgent。而现在的 AI CLI 大量使用内置 fetch。

> 需核实：新版 Node 提供了 `NODE_USE_ENV_PROXY` 之类的开关来改变这一默认行为，具体从哪个版本起、覆盖到哪些 API，实现前实测确认。若可用，L2 对 Node 的适用性会大幅提升。

更可靠的做法是 **preload hook**：

```bash
NODE_OPTIONS="--require /path/to/newgate-hook.cjs"
```

hook 在 tool 代码之前执行，做三件事：

1. 给 undici 装上全局 dispatcher，指向 newgate 代理（**不需要 MITM，走本地明文 HTTP 到我们，再由我们发 TLS 到上游**）。
2. patch 全局 `fetch` / `http.request` / `https.request` 作为兜底。
3. 注入 session 令牌头。

优点：

- **不需要 CA、不需要 MITM**，TLS 由我们这一端正常发起。安全面小得多。
- 覆盖率比环境变量高（直接改运行时行为）。

缺点与边界：

- 只适用于 Node；Bun/Deno 有各自的等价机制（`--preload` / `--import`），需分别实现。
- 编译成单文件二进制（如 bun build --compile）的工具无法 preload → 回落 L2。
- `NODE_OPTIONS` 对部分 flag 有限制，且某些工具会自己覆写它 → 注入前后校验。
- hook 脚本的路径与内容要防篡改（放 0700 目录，启动时校验哈希）。

**优先级：Node 系 tool 用 L3，其余用 L2。**

## 5. 接管等级的选择与降级

```
L1 虚拟模型命名空间  (已适配 + 工具接受自定义 provider)
  ↓ 工具拒绝未知模型名 / 未适配
L3 进程内劫持        (Node/Bun/Deno 运行时)
  ↓ 非 Node 运行时 / preload 不可用
L2 透明拦截          (需用户显式开启 MITM，首次要确认 CA)
  ↓ 证书固定 / 流量绕过
L0 baseUrl 重定向    (最保守，能力最弱)
  ↓
报错并给出具体诊断
```

同样的原则：**每一次降级都要说出来**。用户以为在用 L1 热切换、实际退回了 L0 需要重启，这种落差不解释清楚就是 bug。

## 6. 会话级路由上下文

代理需要知道「这个请求来自哪个 session」，才能支持「五个终端各用各的 group」。

注入时给每个 session 发一个短期令牌，令牌绑定 `(sessionId, tool, groupId)`。代理据此查路由，而不是查全局默认值。

这带来一个 cc-switch 类工具结构上做不到的能力：**同一台机器上，五个 opencode 会话，各自绑不同的 Preset，互不干扰**，并且都能被单独热切换。

## 7. 明确不做

- **系统级 tun/eBPF 全局透明代理**：需要 root/内核权限，故障时会打断用户的全部网络，收益不足以抵消风险和维护成本。
- **把 CA 装进系统信任库**：见 §3.2。任何「一键信任」的便利都不值得。
- **默认开启 L2**：MITM 必须是用户明确知情后开启的。首次开启要有一屏说明：我们会看到什么、CA 存在哪、如何撤销。

## 8. 测试

| 场景 | 断言 |
| --- | --- |
| L1 名称解析 | `newgate/opencode.search` → 正确的 (provider, model)；未知 role → 明确错误 |
| L1 热切换 | 会话进行中改绑定 → 下一个请求即生效，无需重启 |
| L2 劫持名单 | 命中域名被改写；未命中域名（如 registry.npmjs.org）逐字节透传 |
| L2 CA 作用域 | 断言系统信任库未被修改；非 newgate 子进程无法验证该 CA |
| L2 pinning | 用固定证书的假客户端 → 断言给出的是「证书固定」诊断而非超时 |
| L3 hook | 假 Node tool 用内置 fetch 发请求 → 断言被劫持；断言未设 NODE_EXTRA_CA_CERTS 也能工作 |
| 模糊匹配 | 别名表命中优先于启发式；未命中时默认 passthrough 且产生日志 |
| 并发多 group | 3 个 session 绑不同 group 同时发请求 → 各自路由正确，无串扰 |
| 级联代理 | 配置 upstreamProxy → 断言出口经过它 |
