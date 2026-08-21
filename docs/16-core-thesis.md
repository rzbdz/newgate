# 16 · 核心论点：语义命名层

> **工具配置里只写 `heavy` / `cheap` / `search`。它们指向什么，永远只在一个地方改。**

这一篇是产品的定义。前面 01–15 是零件，[14-competitive-analysis.md](14-competitive-analysis.md) 是为什么现有的东西不够。

## 1. 问题的本质是命名，不是路由

你的痛点还原成一句话：

> 我有 M 个工具 × N 个供应商/plan，每次采购变化、每次模型换代，我要改 M×N 处配置。

标准解法是加一个间接层：

```
M 个工具 ──写死──▶ N 个真实模型          M×N 处要维护
M 个工具 ──写──▶ K 个语义名 ──▶ N 个真实模型   只有 K 处要维护
```

`K` 是语义标签的个数，大概 3~6 个（`heavy` / `cheap` / `fast` / `search` / `title`）。**M 和 N 怎么涨，要维护的地方都还是 K 个。**

这就是全部。newgate 是这个间接层的实现，不多也不少。

### 为什么不是"路由问题"

因为你要的是**声明意图**，不是**自动最优**：

- 「这个任务重要，走 heavy」——你知道你要什么
- 不是「帮我自动挑一个性价比最高的」

OmniRoute 走的是后者（19 种策略、14 因子打分）。那是另一个产品，复杂度也在另一个量级。**显式声明比自动优化简单一个数量级，而且不会猜错。**

## 2. 三个平面

```
┌──────────────────────────────────────────────────────┐
│ 绑定表 Binding Table  ★ 唯一的真相                     │
│   (tool, role) → (provider, model)                   │
│   一个人类可读的文本文件。改这里，全世界跟着变。          │
└─────────────────────────┬────────────────────────────┘
                          │
┌─────────────────────────▼────────────────────────────┐
│ 数据面 Proxy                                          │
│   收请求 → 查表 → 改 model 字段 → 转发                 │
└─────────────────────────┬────────────────────────────┘
                          │
┌─────────────────────────▼────────────────────────────┐
│ Bootstrap 引导（每个工具一次）                          │
│   把工具的 baseUrl 指向代理，模型名写成语义标签。         │
│   装一次，此后只读。                                    │
└──────────────────────────────────────────────────────┘
```

### 绑定表长什么样

```jsonc
{
  "providers": {
    "myplan":  { "baseUrl": "https://xxx/anthropic", "key": "env:MYPLAN_KEY" },
    "kimi":    { "baseUrl": "https://api.moonshot.cn/anthropic", "key": "env:KIMI" },
    "local":   { "baseUrl": "http://127.0.0.1:8000/v1", "key": "plain:-" }
  },

  // ★ 换代时只改这里
  "roles": {
    "heavy":  "myplan/claude-opus-4-8",
    "cheap":  "kimi/kimi-k2",
    "fast":   "kimi/moonshot-v1-8k",
    "search": "local/qwen3-32b"
  },

  // 某个工具要偏离默认，才在这里写
  "tools": {
    "opencode": { "search": "kimi/kimi-k2" }
  }
}
```

下个月 opus-5 出来：

```diff
-  "heavy":  "myplan/claude-opus-4-8",
+  "heavy":  "myplan/claude-opus-5",
```

**一行。claude、opencode、hermes、以及任何接进来的工具，全部跟随。**

### Bootstrap 之后工具配置长什么样

```jsonc
// ~/.claude/settings.json —— 写一次，再也不动
{ "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8788",
    "ANTHROPIC_DEFAULT_OPUS_MODEL":   "heavy",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "cheap",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL":  "fast"
}}

// ~/.config/opencode/opencode.json —— 写一次，再也不动
{ "model": "newgate/heavy",
  "small_model": "newgate/fast",
  "agent": { "search": { "model": "newgate/search" } } }
```

> 具体字段以发现流水线的探针实测为准（[05-tools.md](05-tools.md) 核实清单）。

## 3. Preset：整套档位的切换（N4 / N5）

一套完整的 role 绑定叫一个 **Preset**。切 preset = 切一整套成本档。

```jsonc
"presets": {
  "professional": {                          // 默认
    "heavy": "myplan/claude-opus-4-8",
    "cheap": "myplan/claude-sonnet-4-6",
    "fast":  "kimi/moonshot-v1-8k"
  },
  "toy":  { "*": "local/qwen3-4b" },         // 所有档位都用最垃圾的
  "free": { "*": "freetier/glm-4-flash",     // plan 忘续费时的临时救急
            "heavy": "freetier/deepseek-v3" }
}
```

`"*"` 是通配兜底，不必枚举每个 role。也支持 `"extends": "professional"` 只覆盖差异项。

### 3.1 三种切换粒度

| 粒度 | 命令 | 是否改全局状态 |
| --- | --- | --- |
| 全局默认 | `newgate use budget` | **是**，持久化 |
| 某个工具的默认 | `newgate use max --tool claude` | **是**，持久化 |
| **仅这一次调用** | `newgate --preset toy claude` | **否** |

### 3.2 argv0 分发：`newgate-toy claude`

每次敲 `--preset toy` 太啰嗦。**用多调用二进制（busybox 那种做法）**：程序读自己的 `argv[0]`，`newgate-toy` 等价于 `newgate --preset toy`。

```bash
newgate preset install toy professional free   # 在 PATH 里建三个符号链接
```

于是：

```bash
newgate-toy claude              # 临时小活儿，全档位最便宜
newgate-professional claude     # 日常项目
newgate-free claude             # plan 忘续费了，临时顶一下
claude                          # 什么都不加 = 全局默认 preset
```

`newgate-toy` 后面的参数照常透传：`newgate-toy claude --resume abc` 一样工作（[03-cli-spec.md](03-cli-spec.md) §2）。

### 3.3 关键性质：per-invocation 覆盖不改变任何状态

这是 wrapper 真正的价值，比"少打几个字"重要得多。

> **`newgate-free claude` 跑完，世界和跑之前一模一样。**
> 「之后切回来」不是一个动作——**不再敲那个前缀就是切回来了**。

对照现有工具的模型：每次切换都是一次全局写入，用完必须记得切回去。忘了就一直用错档位，而且**不会有任何提示**——直到账单出来。

这条性质也让并发成为可能：

```bash
# 三个终端同时开着，各跑各的，互不干扰
终端 A: newgate-professional claude    # 主项目
终端 B: newgate-toy claude             # 顺手试个小东西
终端 C: claude                         # 用全局默认
```

### 3.4 实现：wrapper 只做一件事

wrapper **不碰任何文件**。它把 preset 编码进工具要访问的 URL 路径，然后 spawn：

```
bootstrap 写死的:  ANTHROPIC_BASE_URL = http://127.0.0.1:8788/
newgate-toy 覆盖:  ANTHROPIC_BASE_URL = http://127.0.0.1:8788/p/toy
```

代理从请求路径里读出 preset，查对应的绑定表。**完全无状态**——不需要注册令牌、不需要生命周期管理、代理重启也不受影响。

wrapper 的全部工作：

```
1. 从 argv[0] 或 --preset 解析出 preset 名
2. 校验 preset 存在（不存在立刻报错，不要静默用默认值）
3. 查工具描述符，得到它的 baseUrl 环境变量名
4. 设一个环境变量
5. execve(工具, 透传参数)
```

零文件 IO，零网络请求。启动开销就是一次 exec。

> **兜底**：少数工具的 baseUrl 只能来自配置文件、没有对应的环境变量。这类工具在 `newgate preset install` 时**预生成一个 per-preset 的配置目录**（一次性，只读），wrapper 通过它的 `configDirEnv` 指过去。仍然是零运行时文件 IO。两条路都不通的工具，只能用全局 preset，`newgate doctor` 要明确告知这个限制而不是假装支持。

### 3.5 `newgate-<preset>` 不带工具名时

打印这个 preset 会解析成什么，方便确认：

```
$ newgate-toy
preset: toy
  heavy   → local/qwen3-4b      (来自 "*" 通配)
  cheap   → local/qwen3-4b      (来自 "*" 通配)
  search  → local/qwen3-4b      (来自 "*" 通配)
代理: 127.0.0.1:8788 ✓ 运行中
```

## 4. 这个论点删掉了什么

**这才是"简单"的具体含义——不是少写功能，是问题被重新表述后，整类工作消失了。**

| 原本要做 | 现在 | 为什么 |
| --- | --- | --- |
| 每次运行改配置文件 | **删** | 只在 bootstrap 时写一次 |
| 退出时 rollback | **删** | 一次安装不需要回滚 |
| journal + 崩溃恢复 | **删** | 没有需要还原的现场 |
| inplace 冲突处理 | **删** | 不在热路径上 |
| sandbox 复制配置目录 | **删** | 不改原文件就不需要隔离 |
| 文件锁与并发写 | **删** | 一台机器 bootstrap 一次 |
| **协议翻译（20k+ 行）** | **M1 删** | 同协议直接透传，只改 `model` 一个字段 |
| 自动选最优 / 打分策略 | **删** | 显式声明代替 |
| 压缩 / 记忆 / 免费额度聚合 | **删** | 不是这个问题 |

[04-injection.md](04-injection.md) 里最脏的部分（inplace 冲突、崩溃恢复、格式保真的强要求）全部只剩 L2/L3 兜底路径还用得上。

**代价换来的是：M1 的核心逻辑约等于「一张表 + 一个改 JSON 字段的反向代理」。**

## 5. 自我约束（写进 README 当承诺）

两个主要玩家一个 169k 行一个 336k 行。**在这个赛道，克制本身就是差异化，而且是它们结构上追不回来的——功能删不掉。**

| 约束 | 数值 |
| --- | --- |
| 核心代码 | < 15k 行 |
| 概念数 | ≤ 4：provider、role、binding、tool |
| 依赖 | 单文件二进制，零必需运行时依赖 |
| 配置 | 人可读、可 diff、可进 git 的文本 |
| 上手 | 5 分钟看完全部概念 |

**明确不做**：压缩、自动选最优、免费额度聚合、持久记忆、语音、图像生成、评测。

任何一个功能提案，先问：**它是否让"K 处维护"这个数字变小？** 不是就不做。

## 6. 代价：我们在关键路径上

代理常驻，它挂了这台机器上所有 AI CLI 一起挂——而且用户不会想到是 newgate，因为那行配置是三个月前写的。

这是这个架构**全部的风险来源**。以下不是可选项：

### 6.1 fail-open，不 fail-closed

| 故障 | 行为 |
| --- | --- |
| 绑定表读不出 | 用内存里最后一次可用的表继续跑 |
| role 查不到 | **原样透传**这个模型名，不报错 |
| provider 挂了 | 返回**带 `newgate` 标识和排查命令**的错误 |

### 6.2 常驻要真的常驻

用户级服务（launchd / systemd --user / 计划任务）+ 自动重启；wrapper 发现代理不在就懒启动；`newgate doctor` 第一检查项。

**代理进程要尽可能笨**——复杂逻辑放控制面，数据面只查表改字段。笨 = 稳。

### 6.3 逃生舱是一等功能

```bash
newgate off              # 所有工具恢复直连，代理停掉
newgate off --tool claude
```

必须做到：**代理删掉、newgate 二进制删掉，用户的工具照样能用。** bootstrap 时保留原配置备份，`off` 就是还原它。

> 一个把自己放在所有流量必经之路上的工具，如果不能一键退出，没有人敢装。

### 6.4 bootstrap 写进去的是永久 API

用户不会为了升级 newgate 重新 bootstrap 所有工具。所以：

- 端口固定（选高位，参考 15721 / 20128 的做法），安装时检测冲突
- `newgate/<role>` 这个命名格式是公开契约，永不变更
- 演进只能新增，不能改

### 6.5 流式必须完美

代理在每个 token 的必经之路上。**这是 M1 唯一需要下重手的技术点**——超时要分三段（首字 / 静默 / 总时长，抄 cc-switch `proxy/types.rs:20-40` 的 60/120/600），SSE 逐块转发不能缓冲。

其余都可以简单，这里不行。

### 6.6 工具会覆写自己的配置

登录、更新、改设置时可能冲掉我们写的块。代理启动时校验各工具配置指纹，发现被冲掉就提示重新 bootstrap。这让漂移检测（[10-llm-native.md](10-llm-native.md) §3）从"锦上添花"变成必需品。

## 7. LLM 在这个论点下的位置

[10-llm-native.md](10-llm-native.md) 的能力**不改变核心，只降低接入成本**：

- **发现**：接一个新工具时，自动找出它的配置文件、有哪些模型槽位、该写什么。省掉硬编码适配——这正是 cc-switch 8 个工具就压到 1.4k issues 的原因。
- **诊断**：确定性规则先行，LLM 兜底。

**原则不变：LLM 提出候选，探针裁决。** 而且 LLM 不可用时全部功能降级可用——它是加速器，不是必需品。

## 8. 一句话

> 别人在帮你**管理一堆配置文件**。
> newgate 让你**只维护 5 个名字**。
