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
