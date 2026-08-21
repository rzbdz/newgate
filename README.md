# newgate

> **你的 AI 工具配置里只写 `heavy`、`cheap`、`search`。**
> **它们指向哪个模型，永远只在一个地方改。**

```diff
  # 下个月 opus-5 出来了，改这一行：
-   "heavy": "myplan/claude-opus-4-8",
+   "heavy": "myplan/claude-opus-5",
  # claude、opencode、hermes、以及任何接进来的工具，全部跟随。
```

## 要解决的问题

你有 M 个 AI 工具 × N 个供应商/plan。采购换了、plan 升级了、模型换代了——你要改 M×N 处配置文件。

加一个间接层，就只剩 K 处（K = 语义标签数，大概 3~6 个）：

```
M 个工具 ──▶ K 个语义名 ──▶ N 个真实模型
             ↑ 只维护这里
```

newgate 就是这个间接层。**不多也不少**——详见 [docs/16-core-thesis.md](docs/16-core-thesis.md)。

## 怎么工作

```
1. bootstrap（每个工具一次）  工具配置 → 指向本地代理，模型名写成语义标签
2. 之后                      工具配置再也不动
3. 切换                      改绑定表，所有工具立即生效，不重启
```

```bash
newgate init claude              # 一次性接管，展示 diff 让你确认
newgate set heavy myplan/opus-5  # 改绑定，全局生效
claude                           # 照常用，已经走 newgate
newgate off                      # 一键退出，全部恢复直连
```

**临时换档不改变任何全局状态**：

```bash
newgate preset install toy professional free

newgate-toy claude            # 随手试个小东西，全档位最便宜
newgate-professional claude   # 日常项目
newgate-free claude           # plan 忘续费了，临时顶一下
claude                        # 什么都不加 = 全局默认
```

跑完，世界和跑之前一模一样——**「之后切回来」不是一个动作，不再敲那个前缀就是切回来了**。三个终端可以同时开着各跑各的。

## 四个概念

| 概念 | 一句话 |
| --- | --- |
| `Provider` | 一个上游账号：baseUrl + key |
| `Role` | 语义标签：`heavy` / `cheap` / `fast` / `search` |
| `Binding` | `(tool, role) → (provider, model)`，**唯一的真相** |
| `Preset` | 一整套 binding，可整组切换（`toy` / `professional` / `free`） |
| `Tool` | 被接管的 CLI：claude / codex / opencode / hermes … |

就这些。injection、endpoint、group 等是需要时才出现的高级概念。

## 自我约束

这个赛道现有的两个主要项目，一个 16.9 万行，一个 33.6 万行。我们的承诺：

| | |
| --- | --- |
| 核心代码 | < 15k 行 |
| 概念数 | ≤ 5 |
| 依赖 | 单文件二进制，零必需运行时依赖 |
| 配置 | 人可读、可 diff、可进 git |
| 上手 | 5 分钟看完全部概念 |

**明确不做**：压缩、自动选最优 provider、免费额度聚合、持久记忆、语音、图像生成。

任何功能提案先问一句：**它能让"K 处维护"变小吗？** 不能就不做。

## 差异化

| | |
| --- | --- |
| **语义命名是核心** | 不是附带功能。现有项目要么只对 Claude 有 4 个写死档位，要么把这能力埋在"一模型一 profile"的主路径下 |
| **一次配置，此后只读** | 现有方案的配置改写是会话态（GUI 退出就要还原）。我们是一次性安装 |
| **临时换档零状态** | `newgate-toy claude` 不写任何全局状态，用完不需要"切回来"。多终端并行各用各档 |
| **LLM 只用来降低接入成本** | 自动发现新工具的配置结构；**LLM 提候选，探针裁决**，不可用时降级可用 |
| **中立** | 不做 provider 返利推广 |

## 文档

**从这里开始** → [docs/00-index.md](docs/00-index.md)（阅读顺序、每篇状态、术语约定）

最重要的两篇：

| | |
| --- | --- |
| [16-core-thesis.md](docs/16-core-thesis.md) | **核心论点**：为什么是命名问题，删掉了什么，代价是什么 |
| [14-competitive-analysis.md](docs/14-competitive-analysis.md) | **竞品代码级分析**：cc-switch / OmniRoute，带 `文件:行号` |

设计细节（每篇的当前/过时状态见 [00-index.md](docs/00-index.md)）：

| | |
| --- | --- |
| [01-concepts.md](docs/01-concepts.md) | 概念、类型、解析优先级、Role/Preset |
| [02-architecture.md](docs/02-architecture.md) | 分层、模块边界、启动时序 |
| [03-cli-spec.md](docs/03-cli-spec.md) | CLI 语法、argv0 分发、透传规则、退出码 |
| [04-injection.md](docs/04-injection.md) | 注入机制（现已降级为 bootstrap） |
| [05-tools.md](docs/05-tools.md) | 工具描述符规范 + 各 CLI 适配 |
| [06-config-schema.md](docs/06-config-schema.md) | 磁盘布局、schema、密钥引用 |
| [07-control-plane-api.md](docs/07-control-plane-api.md) | BE API、事件流 |
| [08-gateway.md](docs/08-gateway.md) | 网关：路由、协议翻译、观测 |
| [09-security.md](docs/09-security.md) | 密钥处理、本地服务安全 |
| [10-llm-native.md](docs/10-llm-native.md) | 发现流水线、漂移检测、诊断 |
| [11-proxy-native.md](docs/11-proxy-native.md) | 四级接管、透明拦截、进程内劫持 |
| [12-frontends.md](docs/12-frontends.md) | CLI / TUI / Web + 远程连接 |
| [13-pain-points.md](docs/13-pain-points.md) | 痛点挖掘与优先级 |
| [15-roadmap.md](docs/15-roadmap.md) | 里程碑 + **待拍板问题** |
| [17-testing.md](docs/17-testing.md) | **M0 测试框架**：全本地 mock、零 token |
| [18-role-preference.md](docs/18-role-preference.md) | Profile 链与任务档位（设计中） |

## 状态

设计阶段，尚无代码。竞品已做代码级调研（结论：缺口真实，建议自建但必须极简）。

**下一步是 M0 测试框架**（[17-testing.md](docs/17-testing.md)）：全本地 mock、零 token、CI 不出网。其中的 FakeUpstream 同时就是发现探针的实现，所以它不是前置开销。

待拍板的开放问题见 [docs/15-roadmap.md](docs/15-roadmap.md) §2——最要紧的是**实现语言**（它决定 M0 用什么写）。
