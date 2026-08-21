# 14 · 竞品分析（代码级）

> 方法：`git clone` 后读源码，不依赖 README 自述。
> 抓取时间 2026-08-13。cc-switch commit `1f38c83`，OmniRoute `release/v3.8.50`。
> 所有结论都给出 `文件:行号`，可自行复核。

## 0. 一句话结论

| | cc-switch | OmniRoute | 我们 |
| --- | --- | --- | --- |
| 本质 | AI CLI 的**配置切换器**（GUI） | **免费额度聚合网关**（全家桶） | AI CLI 的**语义命名层** |
| 体量 | 169k 行 Rust（proxy 占 61k） | 336k 行 TS（2182 文件） | 目标 < 15k |
| 能否解决你的核心痛点 | **不能**（架构限制） | **部分能，但被埋住了** | 这就是唯一目标 |

**建议：自己写，但要极简。** 理由不是"它们不好"，而是**它们都不是围绕你的核心需求构建的**——你的需求是一个**命名间接层**问题，它们一个把它写死成 Claude 专用的四档别名，另一个把它埋在 19 种路由策略、12 层压缩、339 个 provider 的功能堆里。

---

## 1. 先把你的痛点形式化

你的原话拆成五条可验证的需求：

| # | 需求 | 形式化 |
| --- | --- | --- |
| **N1** | 一堆工具都要各自维护配置 | 一次接管 N 个工具，之后不再碰它们的配置文件 |
| **N2** | 语义标签而非模型名 | 工具侧只写 `heavy` / `cheap`，不写 `claude-opus-4-8` |
| **N3** | 一处改，全局生效 | 改 `heavy → opus-5` 一次，**所有工具**立即跟随 |
| **N4** | 多 plan / 多供应商并存 | 订阅制 coding plan 与 API key 混用，按任务选成本档 |
| **N5** | 任务粒度切换 | 今天小任务走国产 plan，明天大项目走 API key |

**N2+N3 是核心，其余是配套。** 这两条合起来就是一个经典的间接层：`工具 → 语义名 → 绑定表 → 真实模型`。

---

## 2. cc-switch（代码级）

### 2.1 它的 proxy 确实存在，而且不弱

`src-tauri/src/proxy/` 共 **61,198 行**，不是玩具：

| 能力 | 证据 |
| --- | --- |
| 熔断器 | `proxy/circuit_breaker.rs`、`provider_router.rs:70` 按 `app_type:provider_id` 维护 |
| 故障转移队列 | `provider_router.rs:52-79`，P1→P2 顺序 |
| 协议翻译 | `providers/transform_codex_anthropic.rs`(3020行)、`transform_gemini.rs`(2693行)、`transform_responses.rs`(2402行) |
| 流式 | `providers/streaming*.rs` 五个文件 |
| 超时分层 | `proxy/types.rs:20-40` 首字 60s / 静默 120s / 非流式 600s |
| 用量与计价 | `proxy/usage/` |
| Session 识别 | `proxy/session.rs:47-67` 从 `metadata.user_id`、`x-session-id` 等提取 |

**这块做得比我上一版分析里说的扎实。之前基于 README 的判断偏轻了。**

### 2.2 但它有一个 Claude 专用的「四档别名」

这是最接近你 N2/N3 需求的实现，值得单独看：

```rust
// src-tauri/src/services/proxy.rs:44-47
const CLAUDE_TAKEOVER_HAIKU_MODEL:  &str = "claude-haiku-4-5";
const CLAUDE_TAKEOVER_SONNET_MODEL: &str = "claude-sonnet-4-6";
const CLAUDE_TAKEOVER_OPUS_MODEL:   &str = "claude-opus-4-8";
const CLAUDE_TAKEOVER_FABLE_MODEL:  &str = "claude-fable-5";
```

接管时把这些**稳定别名**写进 Claude 的配置，代理再按档位映射回真实模型（`proxy/model_mapper.rs:70-105`）：

```rust
if model_lower.contains("haiku") { return self.haiku_model }
if model_lower.contains("opus")  { return self.opus_model }
```

**这确实是 N2/N3 的雏形。** 但三个硬限制：

1. **只有 Claude。** 别名常量是 Claude 模型名，映射源是 `ANTHROPIC_DEFAULT_*_MODEL` 环境变量（`model_mapper.rs:24-56`）。Codex/opencode/hermes 没有对应机制。
2. **档位写死为 4 个**，且绑在 Anthropic 的命名习惯上（haiku/sonnet/opus/fable）。你想要的 `cheap`/`heavy`/`search`/`title` 这种自定义语义标签表达不了。
3. **映射是 per-provider 的**，不是全局的。绑定表挂在每个 provider 的 `settings_config.env` 里，换 provider 就换一整套映射——**这正好和 N3「一处改全局生效」相反**。

### 2.3 决定性缺陷：接管是临时态，退出即还原

这是架构层面的，也是我这次读代码最重要的发现。

```rust
// src-tauri/src/lib.rs:1818-1843  cleanup_before_exit()
let needs_restore = has_backups || live_taken_over;
if needs_restore {
    log::info!("检测到接管残留，开始恢复 Live 配置...");
    proxy_service.stop_with_restore_keep_state().await
}
```

含义：

- 代理**跑在 Tauri GUI 进程里**，GUI 退出，代理就没了。
- 所以退出时必须把 tool 的配置**还原回直连**，否则用户的工具全部指向一个死端口。
- 配套的 `LiveBackup`（`proxy/types.rs:130+`）、`ProxyTakeoverStatus`、`live_takeover_active` 全是为这个临时态服务的。

**推论：cc-switch 的「配置文件指向代理」是一个会话态，不是一次性安装。** 你必须让那个桌面 GUI 一直开着。这和「一次配置、此后只读」是两种东西。

### 2.4 对你五条需求的判定

| | 判定 | 依据 |
| --- | --- | --- |
| N1 一次接管多工具 | **半** | 支持 8 个工具，但接管是临时态，退出还原 |
| N2 语义标签 | **不能** | 只有 Claude 的 4 个写死档位，无自定义标签 |
| N3 一处改全局生效 | **不能** | 映射挂在 per-provider 配置里；且多数工具切换后要重启（官方 README 承认） |
| N4 多 plan 并存 | **半** | 有 provider 列表和 OAuth（`codex_oauth`/`xai_oauth`/`copilot_auth`），但同一时刻每个工具只有一个 current provider |
| N5 任务粒度切换 | **不能** | `get_current_provider(app_type)` 是全局单值，无会话维度 |

**你从 README 得出的判断是对的**，代码层面确认：它的数据模型是「工具 → 当前 provider」的全局单态，这决定了 N2/N3/N5 做不了。

---

## 3. OmniRoute（代码级）

### 3.1 它比 cc-switch 更接近你的需求

**336k 行 TS / 2182 个文件**。它确实有真正的间接层：

**① 模型 → 组合 的 glob 映射**（这是最接近 N2/N3 的实现）

```
src/lib/db/repositories/sqliteModelComboMappingRepository.ts
  "Maps model name patterns (glob-style wildcards) to specific combos.
   When a request arrives for a model string like 'claude-sonnet-4',
   the resolver checks all enabled mappings (highest priority first)
   and returns the first matching combo."
```

表结构 `{pattern, combo_id, priority, enabled}`。理论上你可以建一条 `heavy → combo:big-models` 的映射，然后中心改这个 combo——**这就是 N3**。

**② 路由标签**（`src/domain/tagRouter.ts`）

provider 侧带 `tags`，请求侧从 `metadata.tags` 带标签，按 any/all 匹配。

> 但注意 `tagRouter.ts:53-63`：请求标签来自 **body 的 `metadata.tags`**。绝大多数 AI CLI 不会替你塞这个字段——**所以这条路对 CLI 工具实际上是用不上的**，它是给你自己写代码调 API 时用的。

**③ 工具配置写入**：`bin/cli/commands/setup-claude.mjs`、`setup-opencode.mjs`、`configure.mjs`、`launch.mjs`
**④ MITM/TPROXY**：`src/mitm/`（含 `cert/`、`sudoGate.ts`、`upstreamTrust.ts`、`tproxy/`）
**⑤ CLI + TUI + Web + 远程 + MCP** 全都有

坦白说：**我前面为 newgate 设计的 L2 透明拦截、CLI/TUI/Web 三壳、远程模式、MCP server，OmniRoute 都已经有了。** 这些不能再当独有卖点。

### 3.2 但它的 setup 恰好走了和你需求相反的方向

这是决定性的一条。看它怎么给 Claude Code 生成配置：

```js
// bin/cli/commands/setup-claude.mjs:7-8
// fetches the live /v1/models catalog ... and
// writes `~/.claude/profiles/<name>/settings.json` for each supported model

// :48-57
export function buildProfileSettings(modelId, baseUrl, cfg) {
  ANTHROPIC_BASE_URL: baseUrl,
  ANTHROPIC_MODEL: modelId,      // ← 写死一个真实模型 id
  ...
}
```

**它为每一个模型生成一个 profile 目录**，然后你用 `CLAUDE_CONFIG_DIR=~/.claude/profiles/<name> claude` 去选。

也就是说：**选 profile = 选具体模型**。339 个 provider、1200+ 模型 → 一堆 profile 目录。

这是 N2/N3 的**反面**：不是把 N 个模型收敛成 K 个语义名，而是把 K 个入口炸开成 N 个 profile。你想「下个月 heavy 换成 opus-5」，在这个模型下要重新 sync 一遍 profile 并改你启动命令里的 profile 名。

model→combo 的 glob 映射能力是有的，但**产品主路径不是围绕它设计的**，它是给高级用户手工配的一个旁路功能。

### 3.3 它的产品重心不是你的痛点

看它 README 的第一屏和目录结构就清楚了：

- 首屏是 **「~1.51B Free Tokens / Month」**、339 个 provider、90+ 免费额度
- 12 层压缩引擎（RTK/Caveman/LLMLingua-2/OmniGlyph…）
- 19 种路由策略、14 因子打分
- 还有：持久记忆、A2A、语音 push-to-talk、OCR、图像生成、eval harness、43 种语言

**它解决的是「怎么把全世界的免费额度榨干」。** 你的痛点是「我买了 plan，怎么在一堆工具间统一管理」——**这是两个问题**。它顺带能干一部分，但你要为此吞下 336k 行的复杂度和一个每两周变一次的 provider 目录。

### 3.4 两处需要你自己判断的风险

1. **它把 CA 装进系统信任库**（`src/mitm/upstreamTrust.ts`、`sudoGate.ts` 需要提权）。我在 [11-proxy-native.md](11-proxy-native.md) §3.2 明确反对这个做法——它把 MITM 的作用域从「单个子进程」扩大到「整台机器」。这是安全姿态上的实质分歧，不是风格问题。
2. **自述数据无法核实**：1.51B tokens、339 providers、89% 压缩率、25000+ 测试、star 数，全部是维护者自报。README 里有大量推广位与联盟链接。用之前建议自己跑一遍。

### 3.5 对你五条需求的判定

| | 判定 | 依据 |
| --- | --- | --- |
| N1 一次接管多工具 | **能** | 33 个工具的 setup + MITM 兜底 |
| N2 语义标签 | **理论能，实际难** | model→combo glob 映射存在，但主路径是「一模型一 profile」 |
| N3 一处改全局生效 | **理论能** | 改 combo 即可，但要手工配映射，且工具侧配置写的是具体模型 |
| N4 多 plan 并存 | **能** | 四层级联：订阅 → API key → 便宜 → 免费 |
| N5 任务粒度切换 | **半** | 有 `auto/coding`、`auto/cheap` 等变体，但要工具能指定模型名 |

---

## 4. 为什么还是建议自己写

### 4.1 缺口是真实的，而且正好在你的核心需求上

把 N2+N3 单独拎出来看：

```
工具配置里写:  heavy
       ↓
   绑定表（唯一的真相，一处改）
       ↓
   opus-4-8  →（下个月）→ opus-5
```

- **cc-switch**：这个间接层只对 Claude 存在，且档位写死 4 个，绑定挂在 per-provider 配置里。
- **OmniRoute**：间接层存在（model→combo），但产品主路径把它绕开了，工具配置里写的是具体模型 id。

**没有一个把「语义命名」当作产品的核心。** 这就是缺口。

### 4.2 简单是可以论证的，不是自我安慰

你的核心需求需要的东西，列全了就这些：

| 必需 | 说明 |
| --- | --- |
| 一张绑定表 | `(tool, role) → (provider, model)`，几十行 |
| 一个反向代理 | 收请求 → 查表 → 改 `model` 字段 → 转发 |
| 协议透传 | 同协议直接透传，**不翻译**（见下） |
| 一次性 bootstrap | 往每个工具配置里写一次别名 + baseUrl |
| 一个控制面 | 改绑定表 |

对比它们的复杂度来源：

| 复杂度 | 来自 | 你需要吗 |
| --- | --- | --- |
| cc-switch 61k 行 proxy | 每个工具硬编码适配 + 全协议互转 | **不需要**：同协议透传即可 |
| OmniRoute 12 层压缩 | 榨免费额度 | 不需要 |
| OmniRoute 19 策略 14 因子 | 自动选最优 provider | 不需要，你要的是**显式声明** |
| OmniRoute 339 providers | 聚合器定位 | 不需要，你就几个 plan |

**关键洞察：如果只做同协议路由，协议翻译层可以整个不要。** 你的 `heavy` 绑到某个 Anthropic 兼容端点，claude 说 anthropic，直接透传改个 `model` 字段就完事。翻译只在跨协议时才需要，而那是可选增强，不是必需。

cc-switch 的 61k 行代理里，`transform_*` + `streaming_*` 加起来超过 20k 行——**这部分我们 M1 可以是 0 行。**

### 4.3 我们的定位应该收窄

原来那句「AI CLI 的控制平面」太大了，会滑向 OmniRoute 那种全家桶。改成：

> **newgate 是 AI CLI 配置的语义命名层。**
> 你的工具里只写 `heavy`、`cheap`、`search`。它们指向什么，永远在一个地方改。

这句话里没有 provider 数量、没有路由策略、没有压缩、没有免费额度。**这是一个能守住的边界。**

### 4.4 简洁性作为产品主张

这个赛道两个主要玩家一个 169k 行一个 336k 行，各自 1.4k / 323 个 open issues。**「装完 5 分钟能懂全部概念、单文件二进制、配置是人能读的文本」本身就是差异化**，而且是它们结构上追不回来的——功能删不掉。

配套的自我约束（写进 README 当承诺）：

- 核心 < 15k 行
- 概念数 ≤ 4（tool、role、binding、provider）
- 零必需依赖，单文件二进制
- 配置是人可读可 diff 的文本文件，可进 git
- **不做**：压缩、自动选最优、免费额度聚合、记忆、语音、图像

---

## 5. 该吸纳的（都有出处）

这些是读代码读出来的、经过真实使用检验的细节，直接抄：

| 吸纳 | 出处 | 为什么 |
| --- | --- | --- |
| **流式三段超时** | cc-switch `proxy/types.rs:20-40`：首字 60s / 静默 120s / 非流式 600s | 单一 timeout 一定会误杀长思考或漏掉卡死 |
| **稳定别名写进客户端配置** | cc-switch `services/proxy.rs:44-47` | 思路正确，我们把它从 Claude 4 档推广成任意语义标签 |
| **`[1M]` 这类客户端能力标记要剥离** | cc-switch `model_mapper.rs:145+` | 客户端会往模型名里塞本地标记，上游不认 |
| **熔断 key = `app:provider`** | cc-switch `provider_router.rs:70` | 熔断粒度不能只到 provider |
| **日志 URL 脱敏（含 path 里的 key）** | cc-switch `lib.rs` 测试用例，覆盖 userinfo/query/path 三种位置 | 中转服务常把 key 放 path，容易漏 |
| **glob + priority 的映射表** | OmniRoute `sqliteModelComboMappingRepository.ts` | 比精确匹配灵活，比正则可控 |
| **决策头回传** | OmniRoute `X-OmniRoute-Decision` | 用户能看到「这次实际走了谁」，直击「切了没生效」痛点 |
| **端口选高位** | cc-switch 15721 / OmniRoute 20128 | 避开常用端口 |
| **SSRF 防护（阻断 169.254.169.254）** | OmniRoute `config-generator/opencode.ts:14-32` | 任何允许用户填 URL 的地方都要防 |
| **loopback-only 高危路由** | OmniRoute 远程模式：进程 spawn 类接口仅 loopback | 远程模式必须区分只读与可执行 |

## 6. 该丢弃的

| 丢弃 | 原因 |
| --- | --- |
| **代理跑在 GUI 进程里** | cc-switch 的根问题：GUI 退出→代理死→必须还原配置→无法做一次性 bootstrap |
| **协议全互转** | M1 只做同协议透传，翻译作为后续可选项 |
| **CA 装进系统信任库** | 作用域应限于子进程（[11-proxy-native.md](11-proxy-native.md) §3.2） |
| **自动选最优 provider** | 19 策略 14 因子是复杂度黑洞，且「最优」猜错就失去信任。我们要**显式声明** |
| **压缩 / 记忆 / 语音 / 图像 / OCR** | 与核心需求无关 |
| **免费额度聚合** | 不同的产品 |
| **一模型一 profile** | 与语义命名层直接冲突 |
| **README 塞满联盟链接** | 影响 provider 推荐的中立性，企业场景是硬伤 |

## 7. 复核清单

写代码前建议自己验一遍（我读的是源码，不是运行时行为）：

- [ ] cc-switch：GUI 退出后 `~/.claude/settings.json` 是否真的被还原
- [ ] cc-switch：Codex/opencode 是否真的没有等价的档位别名机制
- [ ] OmniRoute：能否只用 model→combo 映射跑通「工具写 `heavy`、中心改绑定」的完整流程；如果能，成本是多少
- [ ] OmniRoute：MITM 是否强制要求系统信任库（有没有 per-process 模式）
- [ ] 两者的 star 数（README 抓取值与第三方报道差一个量级）
