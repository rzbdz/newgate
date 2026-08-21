# 18 · Profile 链与任务档位（设计）

> **两个原始目标**（回到最初的设想）：
> 1. **动态切换**——不重启工具就换模型
> 2. **不同任务用不同 profile**——`cheap` / `expensive` / `cheap-priority` / `expensive-priority`
>
> **实现手段**：给 profile 加 `priority` 连成 fallback 链；profile 内部的档位绑定可以直接写成 list。
> 不新造机制。

**状态：设计，未实现。待 review。**

## 1. 两级 fallback，一个二维结构

```
                    ┌───────── profile 内的 list（横向）─────────┐
  profile           heavy 的候选
  ─────────────────────────────────────────────────────────────
  fable      (5)    [ fable-5 ]                                  ← 稀疏层，只管 heavy
  expensive  (10)   [ opus-5, gpt-5.6-sol, gemini-3.1-pro ]
  cheap      (20)   [ deepseek-v4-pro, gemini-3.1-pro ]
  local      (90)   [ qwen3-32b ]
      │
      └── profile 链（纵向，按 priority）
```

**遍历顺序：profile 为主序，list 为次序（深度优先）。**

```
for P in profiles(按 priority 升序, 已过滤):
    for C in P.roles[role]:            # 单个绑定视为长度 1 的 list
        if 准入(C): 用它
    # P 没定义这个 role → 直接跳到下一个 P（稀疏）
全都不合格 → 见 §8
```

这个顺序是有语义的：**先在当前任务档位内部找替代，找不到才降到下一个档位。** 符合直觉——「我要便宜的」应该先在便宜的里面挑，而不是一失败就跳到贵的。

## 2. 两级各管什么

| 级别 | 表达的意思 | 例子 |
| --- | --- | --- |
| **profile 内 list** | 「在这个任务档位下，这些是我的备选」 | cheap 档位里：deepseek 挂了就用 gemini flash |
| **profile 链** | 「这整个档位不可用了，降到下一档」 | cheap 全挂 → 才动用 expensive |

写在哪一级由**意图**决定，不由技术决定。同一个模型可以同时出现在两级，去重后只试一次（§12 边界 6）。

有了 list，很多「为了 fallback 而存在」的 profile 就不需要了：

```jsonc
// mappings/cheap.json —— 一个文件表达完整的便宜策略
{
  "priority": 20,
  "description": "省钱优先",
  "roles": {
    "heavy":  ["deepseek-relay/deepseek-v4-pro", "gemini-relay/gemini-3.1-pro-preview"],
    "mid":    ["deepseek-relay/deepseek-v4-pro", "openai-relay/gpt-5.6-terra"],
    "light":  ["deepseek-relay/deepseek-v4-flash", "gemini-relay/gemini-3.1-flash-lite-preview"],
    "vision": ["gemini-relay/gemini-3.1-pro-preview"]
  }
}
```

### 兼容现有格式

现在的 `mappings/*.json` 是 `{"provider": "...", "model": "..."}`。新格式要同时接受三种写法，全部等价：

```jsonc
"heavy": {"provider":"deepseek-relay","model":"deepseek-v4-pro"}   // 现状，继续支持
"heavy": "deepseek-relay/deepseek-v4-pro"                          // 简写
"heavy": ["deepseek-relay/deepseek-v4-pro", "gemini-relay/..."]      // list
```

前两种在内部一律转成长度 1 的 list，之后代码里只有一条路径。

## 3. 任务档位：`cheap` vs `cheap-priority` 就是 `pinned`

你原来设想的四种 profile，其实是**两个维度的交叉**：

| | 严格（不外溢） | 优先（可外溢） |
| --- | --- | --- |
| 便宜 | `cheap` | `cheap-priority` |
| 最强 | `expensive` | `expensive-priority` |

「严格」和「优先」的差别就是**要不要往链下面掉**。所以不需要四个独立概念，只需要一个布尔：

```jsonc
{ "priority": 20, "pinned": true, ... }   // cheap           严格：便宜的都挂了就报错
{ "priority": 20, "pinned": false, ... }  // cheap-priority  优先：可以升级到贵的
```

`pinned: true` 的语义是「**我就要这一档，不好用你告诉我，别偷偷换**」。这正是 `cheap`（不许偷偷花钱）和 `expensive`（不许偷偷降质）共同需要的性质。

### 第二个布尔：excluded

```jsonc
{ "priority": 10, "excluded": true, ... }
```

「永不作为别人的 fallback 目标，只能被显式选中」。场景：`expensive` 档位很贵，你不希望某天 cheap 全挂了它自动被用上，把账单打爆。

**两个布尔正交，含义容易混，必须在文档和 TUI 里写死：**

| 标记 | 方向 | 一句话 |
| --- | --- | --- |
| `pinned` | 不往下掉 | 我当链头时，链到我为止 |
| `excluded` | 不被掉进来 | 别人别自动掉到我这 |

`cheap` = `pinned`。`expensive` = `pinned` + `excluded`。

### 成本判断是人写的，不估算

`cheap` / `expensive` 里哪个模型算便宜，**由你手写的顺序表达**，我们不引价格表、不估算。理由同 [16](16-core-thesis.md) §5：没有价格表就跳过，绝不猜。

价格表以后要加也是**可选增强**，不是这套机制的前提。

## 4. 动态切换：三种粒度

第一个原始目标。三种粒度都基于同一套链，只是链头不同：

| 粒度 | 命令 | 改全局状态？ |
| --- | --- | --- |
| 全局默认 | `newgate --set-profile cheap` | 是，持久化 |
| **仅这一次** | `newgate --preset expensive opencode` | **否** |
| **仅这一次（前缀式）** | `newgate-expensive opencode` | **否** |

后两种来自 [16](16-core-thesis.md) §3——per-invocation 覆盖不写任何全局状态，「切回来」不是一个动作。这正好对上「不同 task 用不同 profile」：

```bash
newgate-cheap opencode        # 随手改个小 bug
newgate-expensive opencode    # 重构核心模块
opencode                      # 用全局默认
```

三个终端同时开着各跑各的，互不干扰。

> **术语提醒**：[16](16-core-thesis.md) 把这东西叫 `preset`，代码和 `mappings/*.json` 叫 `profile`，是同一个东西。**建议统一成 `profile`**（代码已经这么写了），16 那边的措辞要跟着改。这个漂移现在就该修，晚了更贵。

## 5. 请求进来的模型名有三种

| 进来的 model | 解析 |
| --- | --- |
| `heavy` / `light` … | **档位名**，走 §1 的二维遍历 |
| `claude-opus-5` | **具体模型名**，走「哪些 provider 提供这个确切模型」的链 |
| 其它 | 默认 400 并列出可用档位；`passthrough` 策略可原样转发 |

### 具体模型名绝不替换成别的模型

`claude-opus-5` 的链是「哪些 provider 有 opus-5」，**不是**「opus-5 挂了给你 sonnet-5」。

用户点名要 opus-5，给他 sonnet-5 是**静默降质**——他只会觉得模型今天变笨了，完全无从追查。跨模型替换必须显式：

```jsonc
"aliases": { "claude-opus-5": "heavy" }   // 显式声明：把它当档位处理
```

这需要 `providers.json` 加 `models: [...]` 字段，可以后置（Q4）。

## 6. 什么算失败、要不要往下走

链上每一步失败后是否继续：

| 上游返回 | 往下走 | 理由 |
| --- | --- | --- |
| 连接失败 / 超时 | ✅ | 可用性问题 |
| `429` | ✅ | 限流，换一家 |
| `500/502/503/504/529` | ✅ | 上游故障 |
| `404` 模型不存在 | ✅ | 这家没这个模型 |
| `401/403` | ✅ **但大声告警** | 凭证坏了不该让请求死，但必须让用户知道是 key 问题不是模型问题 |
| `400` schema 校验 | ⚠️ 默认**不走**，见下 | |
| 其它 `4xx` | ❌ | 请求本身有问题，换谁都一样 |

### 400 是真两难

现场案例：opencode 的 tool schema 在 deepseek 上 400、claude 上正常。所以 400 **有时**换一家真能成。

但若是客户端自己的 bug，往下走就是拿一个坏请求撞遍所有 provider，每家可能 40 秒。

**建议默认 `false` + 提供 `fallbackOn400` 开关。** schema 类问题应该在 [schemarepair](../go/internal/schemarepair/) 里根治（已经这么做了），而不是靠换 provider 掩盖。开了必须每次告警。

### 已开始流式输出就绝不再换

老规矩，写进代码注释：一旦往客户端吐了第一个字节，链到此为止。否则会出现半个 A 模型半个 B 模型的响应。

### 链的成本上界

链无界 = 一个请求串 6 家 × 40 秒 = 4 分钟才失败。两道闸：

```jsonc
"chain": { "maxAttempts": 3, "totalBudgetMs": 120000 }
```

`maxAttempts` 算的是**实际发出的请求数**（跨两级累计），不是 profile 层数。

## 7. 禁用与容忍度：作用在每一步上

### 禁用必须带 TTL

```bash
newgate off opus-5 4h            # 4 小时后自动恢复
newgate off anthropic-relay/* 1d      # 整个 provider 停一天
newgate off heavy:opus-5         # 只在 heavy 档位禁用
newgate on opus-5
newgate off --list               # 禁了哪些、还剩多久
```

**永久禁用会变成「三个月前禁的早忘了，一直纳闷为什么不用 opus」**——和「切了没生效」是同一类故障。默认给 TTL，`--forever` 才永久。

这也正好是你要的「用户可以 disable 这个模型，那么就切换到下一档」——禁用只是链的一个过滤器，落到下一档是自动的。

### probe 发现挂了 → 一键屏蔽 4 小时

`newgate probe` 已经能测出谁挂了、谁太慢。缺的是从「看到」到「处理掉」这一步。

```
$ newgate probe

[ 7/10] 🔴 openai-relay/gpt-5.6-luna          500  14973ms  分组 auto 下模型 gpt-5.6-luna 的可用渠道不存在
[ 8/10] 🟡 anthropic-relay/claude-opus-5        200  44097ms
...

发现 1 个不可用、1 个太慢。要屏蔽吗？

  #  目标                          问题              建议 TTL   屏蔽后这些档位会改用
  1  openai-relay/gpt-5.6-luna       500 渠道不存在      24h       light → deepseek-v4-flash (0.6s)
  2  anthropic-relay/claude-opus-5     44s 超预算 15s       4h        heavy → gpt-5.6-sol (8.9s)

  [1] 屏蔽第 1 个   [2] 屏蔽第 2 个   [a] 全屏蔽   [n] 都不   [e] 改 TTL
>
```

**最后一列是这个交互的重点。** 你按下确认时想知道的不是「屏蔽了 luna」，而是「**屏蔽了我会用上什么**」。只显示动作不显示后果，用户就得自己在脑子里跑一遍链——那正是这套配置最容易劝退人的地方。

### 建议 TTL 由失败类型决定

一刀切 4 小时是错的：429 是限流，半小时就好了；渠道不存在是供应侧配置问题，一天内不会自己好。

| 探测结果 | 建议 TTL | 理由 |
| --- | --- | --- |
| `404` / 渠道不存在 | **24h** | 供应侧配置问题，短期不会自己好 |
| `500/502/503` | **4h** | 上游故障，可能恢复 |
| `429` | **30m** | 限流，很快恢复。屏蔽 4h 是过度反应 |
| 连接失败 / 超时 | **30m** | 可能只是网络抖动 |
| 🟡 太慢 | **4h** | 主观判断，交给用户 |
| `401/403` | **不建议屏蔽** | 这是 key 的问题，屏蔽模型治不了病。应提示去修凭证 |

`401/403` 那一行是关键：**屏蔽是「这个模型现在不好用」的表达，不是「我的配置错了」的表达。** 混在一起会让用户屏蔽掉一堆好模型，却一直没发现真正的问题是某把 key 过期了。这种情况应该直接说「anthropic-relay 的 key 被拒了，去 providers.json 或环境变量里换」。

### 粒度：单个模型还是整个 provider

- 某 provider 的**部分**模型挂 → 只屏蔽那几个模型
- 某 provider 的**全部**模型挂 → 建议屏蔽整个 provider（`anthropic-relay/*`），一条记录代替四条

### 屏蔽会不会把某个档位掏空

屏蔽前必须检查：某档位的链是否会因此空掉。

```
⚠ 屏蔽 deepseek-v4-flash 后，light 档位将无可用候选。
  当前策略 relax 会放宽延迟约束，实际会用 gpt-5.6-luna (15s)。
  确认？[y/N]
```

宁可多问一句，也不要让用户事后发现某个档位悄悄降级了。

### 记录原因，而不只是记录动作

```jsonc
"disabled": [
  { "pattern": "openai-relay/gpt-5.6-luna",
    "until": "2026-08-21T18:00:00Z",
    "reason": "probe 500: 分组 auto 下模型 gpt-5.6-luna 的可用渠道不存在",
    "at": "2026-08-20T18:00:00Z" }
]
```

`newgate off --list` 要把 `reason` 显示出来。**否则三个月后你会看到一条不知道为什么存在的屏蔽记录**——而这正是「永久禁用」最大的害处，也是我们坚持要 TTL 的原因。

### 到期要说一声

屏蔽到期后模型自动回到候选序里。这个时刻应该被记录：

```
[proxy] openai-relay/gpt-5.6-luna 的屏蔽已到期，重新纳入候选
```

否则用户会困惑「我不是屏蔽了它吗，怎么又在用」。

### 与熔断器的分工

这两个都在「暂时不用某个上游」，但性质完全不同，**不能合并**：

| | 熔断器（已实现） | 屏蔽（本节） |
| --- | --- | --- |
| 触发 | 自动，连续失败 N 次 | **人工 ack** |
| 时长 | 秒到分钟（60s） | 小时到天 |
| 依据 | 真实流量的失败 | probe 结果 + 人的判断 |
| 记录 | 内存 | 落盘，带 reason |
| 目的 | 别撞死的上游 | 表达「这个我暂时不想用」 |

熔断是反射，屏蔽是决策。所以屏蔽必须要 ack——**自动屏蔽 4 小时是危险的**：一次偶发 429 就把一个好模型摘掉四小时，用户还不知道为什么。

### probe 是证据，不是判决

probe 打的是 4 token 的最小请求。它失败不代表真实请求一定失败（反之亦然）。所以：

- 屏蔽永远需要人 ack
- TTL 兜住了判断错误的代价——错了最多难受 4 小时，自己会好
- 可选 `--confirm-twice`：要求连续两次探测都失败才建议屏蔽

### 延迟阈值的命令行语法

先说一个必须绕开的坑：

> **`--block-latency>5s` 和 `--block-latency >5s` 在 shell 里到不了我们手上。**
> `>5s` 被 shell 当成重定向——stdout 被写进一个叫 `5s` 的文件，argv 里只剩
> `--block-latency`。而且是静默的：目录里多个莫名的 `5s` 文件，命令看起来"跑了"。
> 这不是解析问题，是 shell 在我们的二进制启动**之前**就吃掉了它。

所以设计上**绕开 `>`，而不是去解析它**。

#### 接受的写法

| 写法 | 需要引号 | 说明 |
| --- | --- | --- |
| `--block-latency 5s` | 否 | **推荐**。裸值默认就是「大于」 |
| `--block-latency=5s` | 否 | 同上 |
| `--block-latency +5s` | 否 | `+` 是 shell 安全的「大于」记号 |
| `--block-latency gt5s` | 否 | 同上，`gt` / `ge` |
| `--block-latency '>5s'` | **是** | 支持，但必须引号 |
| `--block-latency '>=5s'` | **是** | 同上 |

**裸值即「大于」** 是关键决定：`--block-latency 5s` 读作「屏蔽比 5 秒慢的」，符合直觉且完全不需要引号。`>` 只作为**带引号时的可选糖**存在，不作为主推形式。

#### 值缺失时必须给出针对性报错

如果 `--block-latency` 后面没跟到值，几乎一定是踩了重定向坑。**不能只说「缺少参数」**：

```
newgate: --block-latency 需要一个时长

  你是不是写了 --block-latency>5s ？
  `>` 会被 shell 当重定向吃掉（顺便在当前目录留下一个叫 5s 的文件）。

  改成任一种：
    newgate probe --block-latency 5s
    newgate probe --block-latency '>5s'
```

顺手检查一下当前目录有没有形如 `^\d+(ms|s|m)$` 的新文件，有就一并提示「多半是刚才那条命令生成的，可以删掉」。这种主动提示能省掉一次困惑。

#### 时长必须带单位

走 Go 的 `time.ParseDuration`：`5s` / `500ms` / `1m30s` 都行。**裸数字报错**：

```
newgate: --block-latency 5 缺少单位（要 5s 还是 500ms？）
```

不猜 —— 猜错的代价是屏蔽错一批模型。

#### 比什么延迟

比的是**首字节延迟**，也就是 probe 刚测出来的那个数。不是总时长（理由见前面：探测是 4 token、真实请求 97KB，只有首字节可比）。

#### 更好用的一个变体：`--block-slow`

阈值其实已经在配置里了——每个档位有 `roleBudgets`，再乘 mood 倍率。所以最常用的形式根本不需要填数字：

```bash
newgate probe --block-slow          # 屏蔽「超出自己档位当前预算」的
```

`heavy` 预算 60s、`light` 预算 4s，mood=impatient 时各自 ×0.25。一条命令按各档位不同的标准判断，**而且跟着 mood 走**。

`--block-latency <dur>` 是它的显式覆盖版，用于「我这次就想按 5 秒卡，不管预算」。

#### 三个 flag 正交

| flag | 处理 | 需要值 |
| --- | --- | --- |
| `--block-failed` | 🔴 探测失败的 | 否 |
| `--block-slow` | 🟡 超出自己档位预算的 | 否 |
| `--block-latency <dur>` | 🟡 超过指定阈值的 | 是 |

可以同时给。`--block-latency` 出现时覆盖 `--block-slow` 的判据。

#### 不做统一表达式语言

一度考虑 `--block-if 'latency>5s && provider=anthropic-relay'` 这种。**不做**：一个迷你查询语言要处理引号、优先级、类型，是典型的复杂度黑洞，而实际需求就上面三个 flag 能覆盖。真需要复杂筛选时，`newgate probe --json | jq` 更合适——把组合能力交给已有的工具，不自己造。

### 非交互用法

```bash
newgate probe --block-failed              # 只处理 🔴，用建议 TTL，仍然要 ack
newgate probe --block-slow                # 只处理超出各档位预算的 🟡
newgate probe --block-latency 5s          # 按固定阈值卡
newgate probe --block-failed --block-slow --ttl 4h --yes   # 脚本/cron 用
newgate off --undo                        # 撤销上一批屏蔽
```

`--yes` 只给脚本用，文档里要明确写「交互式使用请不要加」——因为自动屏蔽的风险就在上面那张表里。

### 容忍度：mood

```bash
newgate mood impatient    # 一个词，所有档位立刻重新解析
```

```jsonc
"moods": {
  "patient":   { "latencyMultiplier": 3.0 },
  "normal":    { "latencyMultiplier": 1.0 },
  "impatient": { "latencyMultiplier": 0.25 }
},
"roleBudgets": { "heavy": 60000, "mid": 20000, "light": 4000, "vision": 20000 }
```

**预算放在档位上，倍率放在 mood 上。** 能忍多久取决于任务：`light` 10 秒不可接受，`heavy` 60 秒正常。档位声明固有预算，mood 是全局倍率——一个旋钮同时调所有档位，各自保持相对宽严。这直接回答「某天 10s 他也忍不了」。

### 只用首字节延迟

探测是 4 token、真实请求 97KB，**只有首字节可比**（实测 claude 家族 haiku/sonnet/opus 全是 43–45s，与模型大小无关，说明是通道排队开销）。总时长不能用来做准入判断。**这条要写进代码注释**，很容易被后来者改错。

数据来源：`newgate probe` 冷启动种子 + 真实流量首字节 EWMA 滚动更新，存 `stats.json`（机器本地缓存）。

## 8. 全被过滤光了怎么办

| 策略 | 行为 |
| --- | --- |
| `relax`（**默认**） | 逐条放宽：先忽略成本 → 再忽略延迟 → 最后只看「活着」。**每次放宽都告警** |
| `strict` | 报错，提示放宽 mood 或解禁 |

默认 relax，因为「能跑但慢」几乎总好过「跑不了」。绝不静默：响应头 `X-Newgate-Relaxed: latency`。

**`pinned: true` 的 profile 不适用 relax**——它的语义就是「不好用就告诉我」。

## 9. 抖动与会话粘性

延迟在预算线附近摆动 → 同一会话模型来回跳 → 行为不一致，而且**每跳一次 prompt 缓存就作废**（[16](16-core-thesis.md) §7 实测 2685 → 125 tokens）。

| 机制 | 规则 |
| --- | --- |
| **迟滞** | 超预算 120% 才降级；回到 80% 以下才恢复 |
| **会话粘性** | 同一 session 一旦解析就钉住，除非硬失败。会话结束才重解析 |

session id 从 `X-Session-Id` 取（opencode 会发，日志里见过）；取不到时退化到「按链头 profile 钉住」。

## 10. 可观测性——这就是接口本身

链走了哪几步必须能看见，否则用户永远不知道自己实际在用谁。

```
X-Newgate-Chain: cheap[ds(429)→gemini(ok)]
X-Newgate-Route: heavy -> gemini-relay/gemini-3.1-pro-preview
X-Newgate-Profile: cheap
```

```
$ newgate role heavy

heavy → openai-relay/gpt-5.6-sol        预算 15s (档位 60s × mood impatient 0.25)

  profile              候选                          状态       p50首字节  说明
  fable      (5)       claude-fable-5               ⛔ 已禁用             你 2h 前禁的，剩 2h
  expensive  (10) ⊘    ─ excluded，只能显式选中
  cheap      (20)  ├─  deepseek-v4-pro              🟢 备选     0.9s
                   └─  gemini-3.1-pro-preview       🟢 备选     3.4s
  balanced   (30)  ├─  gpt-5.6-sol                  🟢 选中     8.9s      ← 当前
                   └─  claude-opus-5                🟡 太慢     44s       超预算 15s
  local      (90)      qwen3-32b                    🔴 熔断中             45s 后重试

  想用 fable：newgate on fable-5       想放宽：newgate mood patient
  想强制便宜：newgate --set-profile cheap
```

一屏说清「为什么是它、我该改什么」。做不到这个，这套配置就不可用。

## 11. 配置形态汇总

```jsonc
// mappings/cheap.json
{
  "priority": 20,
  "description": "省钱优先，可外溢",
  "pinned": false,
  "excluded": false,
  "roles": {
    "heavy": ["deepseek-relay/deepseek-v4-pro", "gemini-relay/gemini-3.1-pro-preview"],
    "light": ["deepseek-relay/deepseek-v4-flash"]
    // mid / vision 未定义 → 跳到链上下一个 profile（稀疏）
  }
}
```

```jsonc
// state.json（机器本地，经常改）
{
  "active_profile": "cheap",        // 链头
  "mood": "normal",
  "roleBudgets": { "heavy": 60000, "mid": 20000, "light": 4000, "vision": 20000 },
  "chain": { "maxAttempts": 3, "totalBudgetMs": 120000, "fallbackOn400": false },
  "onExhausted": "relax",
  "disabled": [
    { "pattern": "anthropic-relay/claude-opus-5", "until": "2026-08-20T22:00:00Z" }
  ]
  // fallback_profile 已删除，被链取代
}
```

## 12. 边界清单（实现前逐条确认）

| # | 边界 | 结论 |
| --- | --- | --- |
| 1 | profile 稀疏 | ✅ 未定义的档位跳到下一个 profile |
| 2 | 遍历顺序 | profile 为主序，list 为次序（先内后外） |
| 3 | 同 priority | 按名字排序 tiebreak，保证可复现 |
| 4 | 绑定的三种写法 | 对象 / 字符串 / list，内部统一成 list |
| 5 | `--set-profile X` | X 成为**链头**，其余按 priority 跟随；不改变 X 的 pinned/excluded |
| 6 | 链上重复的 (provider, model) | **去重，只试一次** |
| 7 | 链头本身 excluded | 显式选中优先，excluded 只挡自动 fallback |
| 8 | `pinned` 首层失败 | 直接返回错误，不 relax、不往下 |
| 9 | profile 引用了不存在的 provider | 跳过该条 + doctor 报错，不让整条链死 |
| 10 | 熔断状态 | 作为过滤器，与禁用同级 |
| 11 | `maxAttempts` 口径 | 实际发出的请求数，跨两级累计 |
| 12 | 链走完全失败 | 返回**最后一个**上游的原始错误 + `X-Newgate-Chain` 说明每步为什么 |
| 13 | 具体模型名的链 | 只在提供该确切模型的 provider 间流转，**绝不换模型** |
| 14 | provider 声明 models | `providers.json` 加 `models: [...]`，否则 §5 第二类没法解析 |
| 15 | 未知模型名 | 默认 400 + 列出可用档位，不猜 |
| 16 | 屏蔽把档位掏空 | 先算后果并二次确认，不静默降级 |
| 17 | `401/403` 的探测失败 | **不建议屏蔽**，提示去修 key——屏蔽治不了凭证问题 |
| 18 | 屏蔽 vs 熔断 | 两套并存：熔断是自动反射（秒级、内存），屏蔽是人工决策（小时级、落盘带 reason） |
| 19 | `--block-latency>5s` | shell 会当重定向吃掉。裸值即「大于」，`>` 仅作带引号的糖；值缺失时给针对性报错 |
| 20 | 时长裸数字 | 报错要求单位，不猜 ms/s |

## 13. 与现有实现的差异

| 现有 | 变成 |
| --- | --- |
| `mappings/*.json` 每个 profile 一整套绑定 | 加 `priority` / `pinned` / `excluded`，允许**稀疏**和**list** |
| `state.fallback_profile` | **删除**，被链取代 |
| `--set-profile` | 语义不变（链头） |
| `health.Breaker` | 保留，作为链的过滤器 |
| `probe` | 保留，作为延迟数据种子 |
| `providers.json` | 加 `models: [...]`（可后置） |

**净变化很小**：加三个 profile 字段、一个 provider 字段，删一个 fallback_profile。这是这条路最大的优点——它是现有结构的自然延伸，不是旁边另造一套。

## 14. 明确不做

- **自动排质量**——「哪个模型更聪明」测不出来。priority 和 list 顺序是人的判断，禁用是「它今天犯傻」的手动杠杆。
- **自动选最优**——[14](14-competitive-analysis.md) §4.2 已论证是复杂度黑洞（OmniRoute 19 策略 14 因子）。
- **猜成本**——没有价格表就跳过成本过滤，不估算。`cheap`/`expensive` 的成本判断由人写的顺序表达。
- **跨会话学习/推荐**——数据太少噪声太大，收益远小于复杂度。

## 15. 待你拍板

**Q1 · `400` 要不要触发 fallback？** 我倾向默认不走 + 开关。但你现场那个 deepseek/claude 差异说明开着可能更实用。

**Q2 · `pinned` / `excluded` 叫什么？** 现在这俩容易混。备选：`strict` / `manual-only`、`no-fallthrough` / `no-fallinto`。

**Q3 · mood 和 priority 谁硬？** 一个高优先级但太慢的 profile，是「超预算被跳过」还是「priority 高所以忍了」？我倾向前者（mood 是硬约束），`pinned` 例外。

**Q4 · 具体模型名（`claude-opus-5`）M1 就做吗？** 需要 provider 声明 models 列表。倾向后置。

**Q5 · `preset` / `profile` 术语统一成哪个？** 倾向 `profile`（代码已如此），改 [16](16-core-thesis.md) 的措辞。

**Q6 ✅ 已定** · 交互式列表里 🔴 默认勾上、🟡 默认不勾但列出来。
有了 `--block-slow` 这个显式 flag，"要不要屏蔽慢的"由 flag 表达即可，
交互式默认不勾就是对的——想屏蔽慢的人会自己加 flag。

**Q7 ✅ 已定** · `--block-slow` 和 `--block-latency` 都留，**前者是主力**：
它复用 `roleBudgets × mood`，不用填数字、各档位按自己标准判断、自动跟着
mood 走。`--block-latency <dur>` 只作显式覆盖用。

## 16. 实现顺序

每步独立可用：

1. **绑定支持 list**（内部统一成 list）+ **profile 加 priority、稀疏、链式 fallback**，替换 `fallback_profile`
   → 这一步就解决主体需求：禁用某档自动落下一档
2. **`pinned` / `excluded`** → 任务档位（cheap / cheap-priority / expensive / expensive-priority）成立
3. **禁用（带 TTL，记 reason）+ `X-Newgate-Chain` 响应头**
   → 配套 **`newgate probe --block-failed`**：ack 时显示「屏蔽后会改用什么」
4. **`newgate role <档位>` explain 视图** —— 先有可解释性，再加复杂度
5. **延迟统计 + 预算 + mood**
6. **迟滞 + 会话粘性**
7. **具体模型名的 provider 链**（需要 provider.models）
8. **TUI 链视图**

**第 1–3 步覆盖 80% 的实际需求**，可以先只做这三步。
