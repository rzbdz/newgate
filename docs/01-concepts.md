# 01 · 领域概念

这份文档定义 newgate 的名词与它们之间的关系。实现里的类型名、配置文件里的字段名、CLI 里的参数名都以这里为准。

## 1. 关系总览

```
Provider ──1:N──▶ Endpoint          (同一账号的不同协议入口)
   │
   └──被引用──▶ Profile ──绑定──▶ Tool ──1:N──▶ Role   (工具内部的模型槽位)
                    │                              ▲
                    │                              │
                    │              Preset ─绑定─┘  (一组 role→模型 的整体切换单元)
                    ▼
               Injection Plan ──apply──▶ Session ──spawn──▶ 子进程
```

一句话串起来：

> **Session** 是「用某个 **Profile**（它指向一个 **Provider** 的某个 **Endpoint**）通过一组 **Injection** 启动某个 **Tool**」的一次运行。

## 2. Provider

一个上游 LLM 服务账号。**凭证的归属单位**。

```ts
interface Provider {
  id: string;                 // 'anthropic-official' | 'kimi' | 'deepseek' | 'local-vllm'
  displayName?: string;
  endpoints: Endpoint[];      // 至少一个
  credential: SecretRef;      // 见 06-config-schema
  models?: ModelMap;          // 逻辑模型名 -> 该 provider 的真实模型 id
  headers?: Record<string, string>;   // 额外请求头（如网关鉴权、路由标签）
  timeoutMs?: number;
  tags?: string[];            // 'cheap' | 'fast' | 'cn' | 'offline'，给 UI 和路由策略用
  enabled?: boolean;
}
```

### ModelMap

不同 provider 的模型 id 千奇百怪。newgate 内部只认三个**逻辑槽位**，由 tool 描述符决定往哪儿塞：

| 槽位 | 含义 |
| --- | --- |
| `primary` | 主力模型 |
| `fast` | 小/快模型（Claude Code 的 small-fast、补全类场景） |
| `reasoning` | 高推理档（可选，provider 没有就回落到 `primary`） |

```jsonc
"models": { "primary": "kimi-k2-0711-preview", "fast": "moonshot-v1-8k" }
```

## 3. Endpoint

Provider 下的一个具体协议入口。**这是让「一个 provider 服务多种 tool」成立的关键抽象。**

```ts
interface Endpoint {
  protocol: Protocol;      // 'anthropic' | 'openai' | 'openai-responses' | 'gemini' | 'custom'
  baseUrl: string;
  default?: boolean;       // 同协议多入口时的首选
  label?: string;
}
```

为什么需要它：Kimi 同时提供 Anthropic 兼容端点和 OpenAI 兼容端点。

- `newgate --provider kimi claude` → claude 要求 `anthropic` 协议 → 选中 kimi 的 anthropic endpoint
- `newgate --provider kimi opencode` → opencode 走 `openai` 协议 → 选中 kimi 的 openai endpoint

**同一个 provider 定义，两个 tool 各取所需，用户不需要配两遍。**

### 协议协商

工具描述符声明 `acceptsProtocols: Protocol[]`（按优先级排序）。解析时取 tool 接受列表与 provider endpoint 列表的第一个交集。无交集 → 报错，并提示两个出路：

1. 换 provider；
2. 启用 gateway 插件做协议翻译（见 [08-gateway.md](08-gateway.md)），此时 newgate 注入的是网关地址，网关对上游说 provider 的原生协议。

## 4. Tool

被 newgate 包装启动的目标 CLI。

> 命名说明：概念名叫 **Tool**（内部代码、描述符文件名用它）；CLI 上暴露给用户的参数是 `--target`，因为 `--set-provider aa --target claude` 读起来更自然。二者是同一个东西，文档里出现 target 即指 tool。是否要在对外文档里也用 "tool" 这个词，见 [15-roadmap.md](15-roadmap.md) 开放问题。

Tool 是**数据驱动的描述符**，不是硬编码的一堆 if-else。内置一份，用户可覆盖/新增。

```ts
interface ToolDescriptor {
  id: string;                      // 'claude' | 'codex' | 'opencode' | 'hermes'
  displayName: string;
  aliases?: string[];              // 'cc' -> claude

  resolve: BinaryResolution;       // 怎么找到这个 CLI 的可执行文件
  acceptsProtocols: Protocol[];    // 按优先级
  injections: InjectionSpec[];     // 支持哪些注入方式，按优先级
  passthrough?: PassthroughRules;  // 参数透传的特殊规则（默认全透传）
  capabilities?: ToolCapabilities;
}
```

完整规范和各 CLI 的适配草案见 [05-tools.md](05-tools.md)。

## 5. Profile

一份**可复用的、命名的绑定**：在 provider 之上叠加覆盖项。

```ts
interface Profile {
  id: string;                    // 'claude-cheap' | 'work' | 'offline'
  provider: string;              // Provider.id
  tool?: string;               // 限定只对某个 tool 生效（可选）
  modelOverrides?: Partial<ModelMap>;
  env?: Record<string, string>;  // 额外注入的 env
  injection?: InjectionKind;     // 强制指定注入方式，覆盖 tool 默认优先级
  args?: string[];               // 每次都追加给 tool 的参数（如 --dangerously-skip-permissions）
}
```

Profile 是可选层。只写 `--provider kimi` 时，newgate 内部合成一个匿名 profile `{provider: 'kimi'}`。

**Provider vs Profile 的区别**：Provider 回答「连哪、拿什么 key」，Profile 回答「这次/这个 tool 具体怎么用它」。

## 5.5 Role 与 Preset

**一个 tool 内部往往不止一个模型槽位。** opencode、hermes 这类工具的配置里可能有十来个：主模型、小快模型、思考模型、搜索模型、视觉模型、生成标题的模型……把它们统统绑到同一个 provider 的同一个模型上是错的（用推理模型生成对话标题是烧钱，用小模型做规划是灾难）。

### Role

Tool 内部的一个模型槽位。**由发现流水线自动枚举出来**（[10-llm-native.md](10-llm-native.md) §2），不是硬编码的。

```ts
interface Role {
  id: string;                  // 'primary' | 'fast' | 'think' | 'search' | 'title' | 'vision'
  tool: string;
  pointer: string;             // 在 tool 配置里的位置，如 '/agent/search/model'
  displayName?: string;
  inferredFrom?: string;       // 发现时该槽位的原值，如 'anthropic/claude-opus-4'
  semantic?: RoleSemantic;     // LLM 推断的语义，用于模糊匹配与默认绑定
}
```

内置语义词表：`primary` / `fast` / `reasoning` / `search` / `vision` / `embed` / `title` / `summarize` / `apply`。发现不出语义的槽位保留原名，不强行归类。

### Preset

**一组 role → (provider, model) 的绑定，可以整组切换。** 这是操作单位。

> **权威定义在 [16-core-thesis.md](16-core-thesis.md) §3**，包括 `"*"` 通配、`extends` 继承、argv0 分发（`newgate-toy claude`）。这里只给数据形态。

```jsonc
{
  "id": "toy",
  "tool": "opencode",            // 省略则对所有 tool 生效
  "bindings": {
    "primary":  { "provider": "kimi",       "model": "kimi-k2-0711-preview" },
    "think":    { "provider": "deepseek",   "model": "deepseek-reasoner" },
    "search":   { "provider": "local-vllm", "model": "qwen3-32b" }
  },
  "fallback": { "provider": "kimi" }      // 未显式绑定的 role 走这里，等价于 "*"
}
```

**Preset 与 Profile 的关系**：Profile 可以引用一个 Preset（`profile.preset`）。简单场景（tool 只有一个模型槽位）不需要 preset，`ModelMap` 的三个槽位就够了。

> Preset 的真正威力要配合 proxy 的**虚拟模型命名空间**才释放——配置文件只写一次，之后所有切换都在控制面完成，tool 全程无感知。见 [11-proxy-native.md](11-proxy-native.md) §2。

## 6. Injection

把「解析出来的配置」变成「子进程能读到的东西」的策略。核心接口：

```ts
interface Injection {
  kind: InjectionKind;                       // 'env' | 'config' | 'composite' | 'proxy'
  plan(ctx: ResolveContext): InjectionPlan;  // 纯函数，可 --dry-run 打印
  apply(plan: InjectionPlan): Applied;       // 产生副作用，返回回滚句柄
  rollback(applied: Applied): void;          // 幂等
}
```

四种实现：

| Kind | 做什么 | 副作用 | 优先级 |
| --- | --- | --- | --- |
| `injection:env` | 只改子进程环境变量 | 无 | 最高，能用就用 |
| `injection:config` | 备份/沙箱化 tool 的配置文件并打补丁 | 有磁盘写入 | 次选 |
| `injection:composite` | 有序组合多个注入，全成或全滚 | 取决于成员 | 组合用 |
| `injection:proxy` | 指向本地 gateway，由网关持有真实凭证 | 无（需网关在跑） | 插件启用时 |

详见 [04-injection.md](04-injection.md)。

## 7. Session

一次运行实例。持有：解析结果、注入回滚句柄、子进程 pid、崩溃恢复日志条目。

```ts
interface Session {
  id: string;
  tool: string;
  resolved: ResolvedConfig;
  pid: number;
  startedAt: string;
  journalPath: string;       // 崩溃恢复用
  status: 'running' | 'exited' | 'orphaned';
}
```

BE 会列出活跃 session 给 Web 前端看（谁在用哪个 provider 跑什么）。

## 8. 解析优先级（Resolution Order）

**这是最容易出歧义的地方，明确定死。**

确定「这次用哪个 provider」，从高到低：

| # | 来源 | 例子 |
| --- | --- | --- |
| 1 | 命令行显式指定 | `newgate --provider kimi claude` / `--profile work` |
| 2 | 环境变量 | `NEWGATE_PROVIDER=kimi` / `NEWGATE_PROFILE=work` |
| 3 | 项目级配置（向上查找最近的 `.newgate.json`） | 仓库里钉死用某个 provider |
| 4 | 该 tool 的用户级默认值 | `--set-provider` 写进去的 |
| 5 | 全局默认 provider | `defaults.provider` |
| 6 | 报错，列出候选并给出设置命令 | — |

确定「用哪种注入」，从高到低：

1. `--injection` 命令行显式指定
2. Profile 的 `injection` 字段
3. 工具描述符 `injections[]` 里第一个**在当前环境可用**的（可用性由各注入的 `probe()` 判定，例如 config 注入要求配置文件路径可写）

确定「用哪个 endpoint」：见 §3 协议协商。

## 9. 命名词汇表（写代码时统一用词）

| 用这个 | 不要用 |
| --- | --- |
| tool / target | tool、app、client |
| provider | vendor、backend、account |
| endpoint | url、host |
| injection | patch、hack、mode |
| session | run、instance |
| passthrough args | child args、rest args |
