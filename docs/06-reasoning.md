# Reasoning continuity

## 1. 那句 400 到底在说什么（2026-09-18 实测重写）

上游原文（两种方言各一句，同一件事）：

```text
The `reasoning_content` in the thinking mode must be passed back to the API.
The `content[].thinking` in the thinking mode must be passed back to the API.
```

**这句话不可信。** 它把「这一轮没有新指令」报成了「推理没回传」。实测（打
真实 `smt-deepseek/deepseek-flash`，每格 3/3，一手数据在发行版仓库
`newgate-ext` 的 `modules/deepseek/st-reasoning.go` 文件头与本文 §2b）结论是：

> 这条 400 的**唯一**触发条件是**尾部形状**——**最后一条 `role:"user"` 消息的
> `content[]` 非空、且里面全是 `tool_result` 块**。
>
> `reasoning_content` / `content[].thinking` 在不在、是什么内容，对结果
> **毫无影响**。

判据的完整形状（三个条件，缺一不可）：

| 条件 | 说明 |
| --- | --- |
| 尾部 | 锚在**最后一条 `role:"user"` 消息**，不是数组最后一项（见 §2c 的 `role:"system"`） |
| 内容 | 该 `content[]` 非空且**全是** `tool_result`；出现任何别的块（`text`/`image`）就算有指令 |
| 历史 | 那条 `tool_result` 引用的 tool call 里，**至少有一个不是这个聚合器自己产出的**（见 §1.1） |

三条全中 → 400。任何一条不中 → 200。

> **2026-09-18 晚些时候才补上的第三维**：光有「裸尾」还不够，见下一节。
> §3 的修复在只有前两维时是「多发了几次没必要的老老实实请求」，不是错误；
> 加上第三维之后它变成了**精确命中**。

### 1.1 第三个维度：tool call 的「出身」

聚合器（newgate 走的那家）把各家上游的 tool call id 统一改写成自己的
`call_00_…` 形态，并且**记得自己发过哪些 id**。续写那一轮它靠这份记忆把该轮的
`reasoning_content` 补回去。遇到**不是它产的** id，它补不上，于是一路漏到
DeepSeek，DeepSeek 报那句话。

实测（`msg[….]` 位置、id 出处各一个变量，其余字节完全相同，每格 3/3）：

| 尾部 | 历史里的 tool id | 结果 |
| --- | --- | --- |
| 裸 `[tool_result]` | 全部是聚合器自产的 `call_00_…` | **200** |
| 裸 `[tool_result]` | 混进一个别家来的 id | **400** |
| `[tool_result, text]` | 混进一个别家来的 id | **200** |
| 裸 `[tool_result]` | 聚合器自己产的、但那一轮它没思考 | 200 |

「别家来的」有几种形态，都是实际抓到的：

| id 形态 | 谁产的 | 真实抓包里出现处 |
| --- | --- | --- |
| `call_00_XXXXXXXX…` | **本聚合器** | 全部正常轮次 |
| `call_<hex>`（如 `call_faadac561213487ebeea7731`） | 聚合器背后的 OpenAI 系上游 | `err-400-req000412` 的 30 个 |
| `toolu_01XXXXXXXX…` | Anthropic 系（走 `smt-claude` 的 Claude） | `err-400-req000464` 的 70 个 |

**位置无关**：陌生 id 出现在历史任何一处都算（第一轮 foreign、最后一轮也
foreign，一样 400）。唯一的要求是它**存在**于历史里。

### 1.2 为什么这个 400 看起来「偶尔才出现」——两个叠加的原因

绝大多数时候它不会发生，因为：

1. **Claude Code 通常不留裸尾**：它要么在 `tool_result` 后面再追加一条
   `role:"system"` 的插话（mid-turn injection：「The user sent a new message
   while you were working: …」），要么在同一轮里带上文字。这些都不是裸尾。
2. **单一上游的会话不会产生陌生 id**：只有 newgate 的 fallback 把一发请求交给
   了别家（`smt-claude` / `minimax` / ark…），那一轮产生的 id 才会污染会话。

两者叠加，才是「用着用着突然来一发」。朋友那边从来没遇到，通常是**没有开
fallback**——它一家的 id 始终自洽，第二条就永远不成立。

历史数据完全对得上：9321 条 `client-sent` 里，形态 400 共 **65 次**（那天
18:00–21:30 四小时集中出现，其余日子基本为 0），而那 65 次里两侧条件都满足的
是少数。`newgate metrics` 的 `breaker.skipped.shape_error` 只涨这个数。

### 1.3 一个真实抓包的完整因果

`dump/err-400-req000412.client-sent.json`（真实 Claude Code，764KB、229 条
消息、UA `claude-cli/2.1.273`）：

```text
msg[  1..158]  tool id 全是 call_00_…   ← 本聚合器
msg[160..225]  tool id 全是 call_<hex>  ← 别家（fallback 那一轮）
msg[227]       call_00_…                ← 又回来了
最后一条 user  = [tool_result]          ← 裸尾
                                    → 400 must be passed back
```

只把一个变量搬走（把那几轮 foreign id 删掉），尾部**仍然是裸 `[tool_result]`**，
同一个请求体立刻 200。这就是第三个维度存在的直接证据。

## 2. Thinkcache

Gateway 在流式响应经过时提取真实 reasoning，并用稳定的消息/tool key 保存。
下一轮请求进入时，DeepSeek 插件在 assistant 消息上查找：

1. 客户端仍带真实 reasoning：保留；
2. cache 命中：回填真实原文；
3. cache 未命中：**一个字节都不补**（2026-09-18 起，见 §2d），跳过这条消息，
   并把「为什么没有原文」分类报进日志。

Anthropic 方言回填 `content[]` 的 thinking block，且必须排在 tool use 前。
OpenAI 方言回填 `reasoning_content`。

## 2b. 尾部形状：判据、被证伪的假设、以及一手修复

### 判据

**两套判据，别混。** §1 那张表是**诊断**判据，回答「这个 400 像不像尾部形状问题」
（三条：锚在最后一条 `role:user`、那块 `content[]` 全是 `tool_result`、且那条
`tool_result` 引用了外来 tool call）。`repairTailShape` 实现的是**修复**判据，
只有两条：

1. 最后一条 `role:"user"` 的 `content[]` 非空、且**全是** `tool_result` 块；
2. 那条 user 轮之后**没有 `assistant`**（有就不碰，见下「跳过二族」）。

**第三条（外来 tool call 的出身）刻意没实现。** 修复要动的字节在尾部，而「出身」
得翻整段历史、比对每个 tool call id 是谁产出的，代价和收益不成比例：实测 3426 次
尾部修复里，修完之后真的撞上 `must be passed back` 的有 **0 次**。两边的代价也不
对称——**过修一次只花客户端一个「继续」，欠修一次是 400 + 静默沿链转移**（下面
「曾被删掉一次」那段就是这种静默的样子）。所以线画在**形状**上，不画在**出身**上。

诊断那条留在 §1，是因为它解释「为什么这个 400 看起来偶尔才出现」——
**诊断宁可多一条，修复宁可少一条**。

这里只记**怎么测出来的**：

- 打真实上游，10 例边界矩阵，每例 3/3；
- 在一份**真实 Claude Code 抓包**上做 V1–V5 变体（V1 400、V2 400、V3 200、
  V4 200、V5 400 `missing field text`）；
- P1–P3 证明「空 `text` 块」那条检查是**逐条消息**查的，不只查尾部；
- A–I 证明 `thinking` 开关、`tools` 在不在**全都无关**；
- M1–M4 证明跨上游迁移时是**同一条规则**（外来 reasoning + 裸尾 400、
  外来 reasoning + 合规尾 200）；
- `signature` 无关；签名占位符从来不会出现在线上字节里。

### 三条被证伪的假设（别再重犯）

1. **不是「数组最后一项」**：Claude Code 会在 `tool_result` 之后追加一条
   `role:"system"` 的插话，数组最后一项是那条 system，被拒的却是前面那条
   user 轮。现场：`err-400-req000464`、`err-400-req000412`。
2. **不是「没有 text 块」**：`[tool_result, image]` 和只有 `[image]` 的尾部
   实测都 200。「全是 tool_result」才是那条线。
3. **不挂在 `thinkingOn` 上**：显式写 `thinking:{"type":"disabled"}`、连
   `tools` 都不带，同一条校验照样触发。

### 一手修复（`repairTailShape`）

命中判据时，往那个 `user` 轮的 `content[]` 末尾追加**一条最简指令**
（`toolLoopRebasePrompt = "继续"`，见 §2d 的措辞说明）。

- **只改「一族」**：那条 user 轮就是数组末尾（最多后面跟一条 `role:"system"`）。
- **跳过「二族」**：裸 `tool_result` 之后还有 `assistant` 的，**故意不碰**。
  实测（用聚合器**真实产出**的 assistant 消息，不是手编的）往那个 user 轮追加
  指令 **3/3 还是 400**。改不好就别改——往用户的对话历史里塞一句模型看不见效果
  的噪音，比 400 更糟。真实客户端也到不了这个形状。
- **不是挂在 `Match` 上的可选项**：它跟着插件走，`newgate st off deepseek`
  可以整手摘掉。

### 曾被删掉一次（记下来）

2026-09-18 当天这一手被**整手删掉**过，理由是它的判据是「请求形状」而不是
「上游真的报了这个错」，加上注入的是一句**英文长句**，会跟着请求进上游、进
对话历史——用户报的是措辞（原话：「就算以后要用，也只用最少字」）。

删掉之后错得很隐蔽：不再有那条 note，日志里也看不到 400——因为上游 400 之后
**自动沿链转移**，客户端拿到的是下一个 provider 的 200。**「看起来没坏」实际是
「每一发都悄悄降级到别的模型」**，而且形状判据认领的那份 `[shape-400]` 证据也
一起没了。

复现路径（下次别再靠猜）——把 `dump/` 里任意一份 Claude Code 的 `client-sent`
原样 POST 到 `/p/ds/v1/messages`，日志里就会出现：

```text
-> 400 ... must be passed back
→ 沿链下一步: minimax          ← 紧接着就转移了，客户端拿到 200
```

**结论：判据是对的，措辞是错的。只改措辞，别删判据。**

### 现状的量

恢复当天（22:18 起）的窗口：**1153 条请求里命中 397 次**。这个比例说明「裸尾」
在真实工作流里**很常见**（Bash 工具轮就是这个形状），不是稀有边角。第 9 章
的 e2e 就是锁这件事：尾部从 `tool_result` 变成 `tool_result,text`，上游收到
的那一句逐字是「继续」。

## 2c. Claude Code 会往 `messages` 里插 `role:"system"`

这不是边角知识，它是 §2b 判据里「锚最后一条 `role:user`」的直接原因，也是
**未来任何按「数组最后一项」写判据的补丁都会踩的坑**。

Claude Code 的用户中途插话（mid-turn injection）会以 `role:"system"` 的身份
出现在 `messages` 数组里，正文形如：

```text
The user sent a new message while you were working: …
```

上游**不认** system 里的指令——它只看最后一条 user。所以：

- 尾部锚点必须是「最后一条 `role:"user"`」，不是 `messages[len-1]`；
- 写 `repairTailShape` 那个下标是因为这个（`rewrite.AppendArrayItemArrayAt`
  按下标插，而不是 `AppendLastArrayItemArray`）。

## 2d. 不该编，不该长

两条都是 2026-09-18 定下来的硬契约，各有实测依据。

### 不编：cache 未命中就跳过，不写占位符

原来 cache 未命中时补一个非空占位符（先是中文长句，后来缩成
`"No thinking in this round"`）。**那整件事是错的**：

上游自己那一轮就没给过推理，那「must be passed back」要求回传的东西
**根本不存在**，我们凭什么替它编一个？编出来的字会进上游、进对话历史、
每轮烧 token，而信息量是零。

实测依据（这才是关键）：`reasoning_content` 的形态对那个 400 **毫无影响**
（真实原文 / 省略字段 / 空串 / 占位符，四格全一样）。也就是说**占位符一个
400 也没救回来**，只是纯粹的编造 + 烧 token。

顺带被推翻的还有官方文档口径——「历史里每条 assistant 都得带非空推理」在
这个上游上**不成立**：两条 assistant 都缺、尾部干净，实测 3/3 是 200。

全库 3494 条历史占位符，逐条拿 tool id 反查「记下本轮推理内容」——**一条都
没命中**，说明这些推理上游从来没给过我们，不是缓存弄丢的。

正确动作：**跳过这条消息**，并把原因分类报进日志（`notool` / `nocache` /
`nokey`，定义见 `skipCause`）。

### 不长：注入的指令只有一个词

`toolLoopRebasePrompt` 从一句英文长句改成一个词（`"继续"`）。

它会被注入的请求、**也会进用户下一轮的对话历史**，越长越像「有人在替我
说话」。用户的原话：「prompt 注入也尽可能用最简单的、不影响流程的，比如
（"继续"）这种」。

必须诚实说清：起作用的是「尾部多了一条**普通用户指令**」这个事实本身，不是
那句话的内容——上游要的是「这一轮有新指令」，不是一个解释。实测同一个请求体
只加一个 `" "`（空格）就 200。

但**空串不行**：`[tool_result, text""]` 会被上游以 `missing field text` 拒掉
（它的解析层把空串等同于字段缺席），而且那条检查是**逐条消息**查的，不只查
尾部。

## 3. 冷层

内存 cache 支持快速查找，`thinkcache.bin` 保存重启后的冷层。冷层损坏时必须报告，
不能伪装成成功恢复。

## 4. 跨 provider tool loop

不同 provider 的 reasoning 状态不一定兼容。优先选择能安全继续当前 tool loop 的
候选；只有安全候选失败后，模型族插件才可执行显式有损 rebase。Rebase 原因和动作
写入日志与 metrics。

**这不是通用限制**（2026-09-16 对同一份真实 thinking + tool_use 做的 A/B）：

| 迁移 | 结果 |
| --- | --- |
| Ark → Ark | 3/3 200 |
| **Ark → DeepSeek** | **3/3 400 must be passed back** |
| DeepSeek → Ark | 3/3 200 |
| DeepSeek → DeepSeek | 3/3 200 |

API 是无状态的；不兼容的是请求里携带的 reasoning/tool 编码。只拦**迁入**
DeepSeek，别把这个上游怪癖扩大成所有 provider 都失去 fallback。

**而 §1.1 给了这件事一个更完整的解释**：迁移本身不是问题，问题是迁移之后
历史里留下了一批**别家产的 tool id**，聚合器在续写那一轮认不出它们。所以
`RebaseToolLoop` 的动作（保留工具结果、追加一条普通用户继续指令）之所以有效，
和 §2b 是一回事：把那一轮变成「有指令的新回合」，绕开「续写」这条路径。

## 5. 不允许的做法

- 不写空串：上游的解析层把空串等同于字段缺席，报 `missing field text`；
- 不凭空伪造「真实推理」：cache 未命中就**跳过**，占位符那条路已删（§2d）；
- 注入的指令不写长句：它进对话历史，越长越像替用户说话（§2d）；
- 不把 provider 特例散落在 forward 热路径；
- **不要按上游的报错文案判据**：这句话的文案与真实原因不一致（§1），
  按文案写会两边都错——修不好该修的，还会误伤不该碰的。
