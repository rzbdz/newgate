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

那句 `must be passed back` **不完全等于**「历史推理没带回来」。2026-09-17 从
两份 958KB 真实 dump（`err-400-req000412/req000464`）里定位到第二个来源：

对话最后一条 user 消息的 `content` 是数组、里面**只有 `tool_result`、一个
text 块都没有**。模型刚拿到一串工具输出，却没被告知「接着干什么」，DeepSeek
的严格校验把这种「没有指令的尾部」误判成推理状态缺失，报的还是同一句话。

判据（`repairTailShape`，modules/deepseek）：

- 只看**最后一条**消息；
- 必须是 `user`，`content` 是数组，且数组里没有 `type == "text"` 的块；
- 命中就追一句用户口气的继续指令：

  ```text
  Continue from the tool results above. Call the next tool you need, or give your final answer.
  ```

只碰这一种形状。已经带文字的尾部、content 是普通字符串的尾部一律不动——
多塞一句只会往用户的对话里加噪音。只在思考模式开着时做（`thinkingOn`）：
关思考的后台小调用（分类器、起标题）根本不带这段历史，改了也没意义。

两个来源的区别决定了排查姿势：**回填问题**看 thinkcache 命中与 `we-sent` 里
的字段，**尾部形状问题**看 dump 里最后一条消息长什么样。两者报错文案相同，
只有一个到不了上游校验那一层时才会暴露是谁。

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
