# Reasoning continuity

## 1. 为什么出现 must be passed back

DeepSeek thinking mode 要求：如果历史 assistant 消息包含 tool call，下一轮必须
把该轮真实 `reasoning_content` 原样传回。Claude Code 对非官方端点可能在会话
历史中剥掉 thinking 块，于是客户端下一轮只发送 tool use，严格上游返回 400：

```text
The reasoning_content in the thinking mode must be passed back to the API.
```

这不是“当前请求没有推理”这么简单，而是历史 tool loop 缺少与之配对的推理状态。

## 2. Thinkcache

Gateway 在流式响应经过时提取真实 reasoning，并用稳定的消息/tool key 保存。
下一轮请求进入时，DeepSeek 插件在 assistant 消息上查找：

1. 客户端仍带真实 reasoning：保留；
2. cache 命中：回填真实原文；
3. cache 未命中：写入明确的非空占位符，避免严格上游直接 400，并记录有损 notes。

Anthropic 方言回填 `content[]` 的 thinking block，且必须排在 tool use 前。
OpenAI 方言回填 `reasoning_content`。

## 2b. 尾部形状：同一个 400 文案的第二个来源

那句 `must be passed back` **不完全等于**「历史推理没带回来」。2026-09-17 定位到
第二个来源（两份 958KB 真实 dump `err-400-req000412/req000464` + 一轮打真实
上游的 A/B，每格 3/3，读 `X-Newgate-Chain` 上 deepseek 那一发的结论）：

**最后一条 `role:"user"` 消息**的 `content` 是数组、里面**全是 `tool_result`
块**（一个 text / image 都没有）。模型刚拿到一串工具输出，却没被告知「接着干
什么」，DeepSeek 的严格校验把这种「没有指令的尾部」误判成推理状态缺失，报的
还是同一句话。

判据（`repairTailShape`，modules/deepseek）：

- 锚在**最后一条 `role:"user"` 消息**，不是数组的最后一项；
- 它之后**没有** assistant 消息；
- 那块 `content` 是数组、非空、且**每个块都是 `tool_result`**；
- 命中就追一句用户口气的继续指令：

  ```text
  Continue from the tool results above. Call the next tool you need, or give your final answer.
  ```

### 实测矩阵（2026-09-17，`/p/ds` 打 smt-deepseek/deepseek-flash，每格 3 轮）

| 尾部形状 | deepseek 那一发 |
| --- | --- |
| 最后一条 user 只有 `[tool_result]` | **400** |
| 同上，数组以 `role:"system"` 插话收尾 | **400** |
| 同上，但 `thinking:{"type":"disabled"}`、**不带 tools** | **400** |
| `[tool_result, tool_result]`（并行工具轮） | **400** |
| `[tool_result, text]` | 200 |
| `[tool_result, image]` | 200 |
| `[image]`（只有图片） | 200 |
| 最后一条 user 带文字 | 200 |

三条结论都是从这张表来的，且都**推翻了旧实现的假设**：

1. **不是「数组最后一项」**。Claude Code 会在 tool_result 之后追加一条
   `role:"system"` 的插话（「The user sent a new message while you were
   working: …」），数组最后一项是那条 system，而被拒的是它前面那条只有
   tool_result 的 user 轮——上游不认 system 里的指令。现场 `req000464`。
   旧实现要求数组最后一项就是 user，**这一族全部漏修**（dump 里 6 份
   `must be passed back`，两份是这种形状）。
2. **不是「没有 text 块」**。`[tool_result, image]` 和只有 `[image]` 的尾部
   实测都是 200。旧判据会把它们也改掉。「全是 tool_result」才是那条线：除
   tool_result 之外的任何块（text、image）都算「有指令」，上游就放行。
3. **不挂在 `thinkingOn` 上**。思考显式关掉、连 tools 都不带，照样 400。
   旧实现放在 `if thinkingOn` 里，后台小调用那条路修不到。

另外两种尾部**故意不修**：

- 数组以 `assistant` 收尾（带 tools 时）。那是另一条规则在管，而且追加指令
  证明**没有用**：实测「tool_result-only 的 user 轮 + 尾随 assistant + tools」
  3/3 400；把继续指令追加到那个 user 轮上（正是本文档的修法）3/3 **还是**
  400——那一族跟「尾部有没有指令」无关。既然改不好就别改，往用户对话里塞一句
  没效果的噪音比 400 更糟。这一族从真实客户端到不了：dump 里全部 15 份
  `err-400` 没有一份是 assistant 收尾。
- 尾部本来就有文字 / 图片的：它本来就能过。

排查姿势：**回填问题**看 thinkcache 命中与 `we-sent` 里的字段，**尾部形状问题**
看 dump 里最后一条 user 消息长什么样。两者报错文案相同。

同一天还归档出三类**文案不同**的 400，都不是这个根因，别混进来：
`[1210] 该模型始终思考，不支持关闭思考`（glm）、`invalid thinking: only
type=enabled is allowed for this model`（kimi）、`Mismatch type
***.ClaudeContent with value string`。见 `docs/11-troubleshooting.md` §1。

## 3. 冷层

内存 cache 支持快速查找，`thinkcache.bin` 保存重启后的冷层。冷层损坏时必须报告，
不能伪装成成功恢复。

## 4. 跨 provider tool loop

不同 provider 的 reasoning 状态不一定兼容。优先选择能安全继续当前 tool loop 的
候选；只有安全候选失败后，模型族插件才可执行显式有损 rebase。Rebase 原因和动作
写入日志与 metrics。

## 5. 不允许的做法

- 不写空字符串：严格上游仍视为缺失；
- 不静默删除字段：会改变 thinking 语义；
- 不凭空伪造“真实推理”：占位符必须明确是恢复兜底；
- 不把 provider 特例散落在 forward 热路径。
