# 19 · 接管机制原理（how it hooks）

> 本文讲**原理**：newgate 到底用哪几种机制把 CLI 拦下来、每种机制挂在哪一环、
> 为什么是这个机制而不是别的。实现细节看代码，`文件:行号` 都给出来了。
>
> 一句话总纲：**所有机制最终都收敛到「把客户端的请求指向本地代理」这一件事**。
> 机制只是「怎么让 CLI 心甘情愿地把 baseURL 换成 localhost」的不同手段。

## 0. 为什么需要不止一种机制

claude 和 opencode 是两类完全不同的客户端，它们的「可配置面」不一样：

| 客户端 | 能不能改配置文件的 baseURL | 能不能被 env 覆盖 | 所以用 |
| --- | --- | --- | --- |
| claude (Claude Code) | 能，但 env 优先级更高 | **能**，且 env 盖过一切 | PATH shim + env 注入 |
| opencode | 能，且它就认配置文件 | 不认 env | 改配置文件 |

选哪一个不是拍脑袋，是被**优先级**逼出来的：

- claude 的 `settings.json` 里有 `env` 块，但**用户 shell 里 export 的变量优先级更高**。
  写配置文件会被 shell 环境静默盖掉——这是最难查的那类故障。所以对 claude
  我们用 env 注入（`internal/runtime/injection/shim.go:24-27`）。
- opencode 没有 env 这一层，它读 `opencode.json`，所以对它只能改配置文件。

## 1. 机制一：PATH shim + argv0 分发（claude 的入口）

**挂在哪**：命令行解析。用户敲的 `claude` 不再直接命中真 claude。

**怎么挂**（`internal/runtime/injection/shim.go:28-46`）：

```
1. 在 ~/.config/newgate/bin/ 放一个符号链接 claude → newgate 二进制
2. 往 shell rc（.zshrc/.bashrc/…）追加一段：把该目录前置到 PATH
3. 于是 `claude` 命中 shim → 跑到 newgate 的 main()
```

**怎么认出来是「被当 agent 调用」而不是「控制命令」**（`cmd/newgate/main.go:18-21`）：

```go
name := filepath.Base(os.Args[0])       // 看 argv0，不是 argv[1]
if a, ok := agents.Get(name); ok {      // "claude" 是已知 agent
    runWrapper(a, os.Args)              // → launch.Launch
    return
}
```

同一个二进制，靠 **argv0** 分身份：

| argv0 | 行为 |
| --- | --- |
| `claude` | wrapper：注入 env 后 exec 真 claude |
| `newgate-ds` | 控制 CLI，等价 `newgate --preset ds` |
| `newgate` | 控制 CLI，正常解析 argv[1] |

**为什么用 shim 而不是改 claude 的配置**：见 §0。shim 在**子进程**里显式设
env，天然盖过用户 shell 里的一切残留变量。

**递归保护**（`launch.go:47-50`）：env 里带 `NEWGATE_DEPTH`，每过一层 +1，
`≥2` 或 `NEWGATE_DISABLE=1` 就直接透传，不注入。防止 shim 调到 shim。

## 2. 机制二：env 注入（核心，claude 真正起作用的地方）

env 注入是 newgate 的心脏。它做的事：**把「模型」这件事从 CLI 手里拿走，
换成一层档位名**。

**注入什么**（`internal/agents/agents.go:82-97`）：

| 角色 | 环境变量 | 注入的值 | 谁在用 |
| --- | --- | --- | --- |
| baseURL | `ANTHROPIC_BASE_URL` | `http://127.0.0.1:8899/a/claude` | 所有请求 |
| 鉴权 | `ANTHROPIC_AUTH_TOKEN` | `newgate-local`（**占位串**） | 所有请求 |
| opus 槽 | `ANTHROPIC_DEFAULT_OPUS_MODEL` | `heavy` 或真实模型名 | 主循环、plan |
| fable 槽 | `ANTHROPIC_DEFAULT_FABLE_MODEL` | `heavy` | 第三方 provider 识别兜底 |
| sonnet 槽 | `ANTHROPIC_DEFAULT_SONNET_MODEL` | `mid` | Bash 分类器、/compact |
| haiku 槽 | `ANTHROPIC_DEFAULT_HAIKU_MODEL` | `light` | 后台小活 |
| subagent | `CLAUDE_CODE_SUBAGENT_MODEL` | `mid` | 所有 subagent |

**三个关键设计**，逐条说：

### 2.1 baseURL 走路径，不走 header（`forward.go:337-351`）

```
/a/<agent>[/p/<profile>]/v1/...
```

为什么是路径不是 header：**claude / opencode 都不允许我们往它们的请求里塞
自定义 header**，但 baseURL 是 bootstrap 时我们自己写进去的。路径方案完全
无状态——代理不需要注册会话、不需要令牌生命周期，重启也不影响在跑的会话。

`/a/<agent>` 让代理知道请求来自谁（per-agent profile）；`/p/<profile>`
是本次调用的 profile 覆盖（`newgate claude --profile glm`），不改全局状态。

### 2.2 auth token 是占位串，真 key 在代理手里

`BuildEnv(port, "newgate-local")`（`agents.go:144`）——**claude 永远拿不到真
API key**。真 key 在 `providers.json`，代理转发时在
`forward.go` 的 `setAuth` 里按链步挂上。

这是安全模型的一部分（docs/09）：**代理持有全部凭证**，客户端只持有一个
在自己机器上、没有任何价值的占位串。

### 2.3 动态档位名 vs 钉死真实名（`launch.go:86-139`）

这是最容易看错的一处。**默认注入的是档位名**（`heavy`/`mid`/`light`），
不是真实模型名。

- **默认（动态）**：槽位 = 档位名。会话把 `heavy` 发回来，代理按**请求时**的
  当前配置解析成真实模型。于是 `newgate --set-profile glm` 对**已经跑着的
  会话立刻生效**——因为它发的一直是 `heavy`，变的只是代理怎么解释 `heavy`。
- **钉死**（显式 `--profile` / `NEWGATE_PROFILE` / argv0 分发）：槽位换成该
  profile 链头的**真实模型名**。本次调用已被钉住，界面显示的就是真用的。

两种模式都在 baseURL 上分歧：钉死时路径里追加 `/p/<profile>`。

### 2.4 unset：比注入更容易被忽略的一半（`agents.go:96-97`）

```go
UnsetEnv: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"}
```

`execReal` 在 exec 前会**主动从子进程环境里删掉**这三个
（`launch.go:159-180` 的白名单逻辑：继承 → 丢干扰项 → 叠加我们的）。

不删的后果就是「切了没生效」最经典的现场：

- `ANTHROPIC_API_KEY` 与 `AUTH_TOKEN` 并存 → 优先级未定义，可能用错那个；
- `ANTHROPIC_MODEL` / `ANTHROPIC_SMALL_FAST_MODEL` 是**单槽旧字段**，
  会盖过全部 `DEFAULT_*_MODEL`，客户端退回单一模型，分类器/subagent 全错。

**修复「切了没生效」，先查这三个变量，不是先查 newgate。**

## 3. 机制三：改配置文件（opencode 的入口）

opencode 不认 env，只能改它的配置文件（`internal/runtime/injection/config_file.go`）：

```
~/.config/opencode/opencode.json
~/.config/opencode/oh-my-openagent.json
```

做法：把文件里指向上游的 provider/model 改写成指向 `http://127.0.0.1:8899`。
JSON 保留注释（jsonc）地做字节级替换，不是整份重写。

**两条防护**：

1. **首次接管前备份**到 `backups/original/`，`newgate off` 时还原。
2. **拒绝污染备份**（`config_file.go:38-62`）：如果目标文件里已经出现
   `"newgate"`，而 `original/` 又不在了——**拒绝对它备份**。否则会把一份
   已被接管的文件当成「原始」存起来，之后 off 还原出来的就是脏版本，用户
   的原配置永久丢失。宁可不备份也不污染。

## 4. 机制四：代理侧 hook（请求进来之后）

前三节是「怎么把请求骗到代理」。请求到了之后，代理内部还有几层 hook：

### 4.1 路由改道（routing hook）

`special.RouteTier`（`st-claude-bg.go:68`）在建链**之前**问一句：这次请求该按
哪个档位建链？它只认一件事——Claude Code 的 Bash 安全分类器（system 里自报
`"You are a security monitor"` 的非流式请求）整条链改走 `light`。

为什么放在建链之前而不是 body 改写里：**改道换的是整条 fallback 链**，body
改写只能换链头（model 字段），换不了链的尾巴。

### 4.2 body 改写（special_treatment 插件）

`gateway/special` 是一串插件，每个 `Match` 认领自己的上游、`Apply` 做**纯字节
手术**（绝不 JSON 往返，保证 message 内容逐字节不变）。当前四个：

| 插件 | 认领条件 | 干什么 |
| --- | --- | --- |
| `claude-bg` | claude + 非流式 | 后台调用补 `thinking:disabled` |
| `deepseek` | 模型/provider/URL 含 deepseek | 补回被客户端剥掉的思维链 |
| `glm` | 含 glm | 缺省 thinking 补显式 disabled |
| `always-thinks` | quirk 注册表命中 | 「始终思考」模型收到 disabled → 翻回 enabled+effort |

**顺序即文件名序**（`claude-bg` → `deepseek` → `glm` → `always-thinks`），
因为插件是 `init()` 里按文件注册的。

### 4.3 quirk 学习（错误驱动的补丁）

有些毛病静态看不出来（`[1210]该模型始终思考`），只能**撞一次才知道**。
`quirk.Learn`（`quirk/quirk.go:133`）从 4xx 报错签名里认出毛病、记进内存注册表，
下次请求 `always-thinks` 就带上补丁——**一次失败换永久免疫**。

注意：注册表**只在内存**，进程重启就忘。这是已知缺口（要持久化得落到用户的
配置文件，而我们不该悄悄往里写）。

### 4.4 thinkcache 观察（响应侧 hook）

`gateway/thinkcache` 旁路观察每个响应（SSE 或非流式），把模型吐出的
`reasoning_content` 按 `tool:<id>` / `text:<hash>` 存进 LRU。下一轮请求里
DeepSeek 要求逐字回传思维链时，`st-deepseek` 就从这里取回原文——
**客户端剥掉的思维链，在代理这一层被记住并补回**。

## 5. 全景图

```
用户 shell
   │  export PATH=~/.config/newgate/bin:$PATH     ← shim 装的
   ▼
claude (argv0)
   │  main() 看到 argv0=="claude" → wrapper
   ▼
launch.Launch                                    ← 机制一：argv0 分发
   │  ① 懒起 daemon（没跑就起）
   │  ② buildInject：算要注入的 env               ← 机制二：env 注入
   │     · ANTHROPIC_BASE_URL = 127.0.0.1:8899/a/claude
   │     · ANTHROPIC_AUTH_TOKEN = "newgate-local"（占位）
   │     · ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL = 档位名
   │     · 删掉 ANTHROPIC_API_KEY / MODEL / SMALL_FAST_MODEL
   │  ③ syscall.Exec(真 claude, 注入后的 env)
   ▼
真 claude 发请求 → 127.0.0.1:8899/a/claude/v1/messages
   ▼
代理 forward                                      ← 机制四：代理侧 hook
   │  ① parseTarget：从路径读 agent / profile
   │  ② RouteTier：要不要改道（分类器）
   │  ③ 建链 → 逐个候选
   │  ④ special 插件改 body（纯字节手术）
   │  ⑤ setAuth：挂上真 key，转发上游
   │  ⑥ thinkcache 旁路记下思维链
   ▼
上游（真实模型）
```

## 6. 手动配置对照（不用 newgate 时）

如果有人想手动把 Claude Code 指向一个 Anthropic 兼容端点，等价于**手动做
机制二**这一件事：

```bash
# 必须设
export ANTHROPIC_BASE_URL="https://your-endpoint.example.com"   # 不带 /v1
export ANTHROPIC_AUTH_TOKEN="sk-ant-xxxx"
export ANTHROPIC_DEFAULT_OPUS_MODEL="claude-sonnet-4-20250514"
export ANTHROPIC_DEFAULT_SONNET_MODEL="claude-sonnet-4-20250514"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="claude-haiku-4-20250414"

# 必须 unset（newgate 帮你做的另一半）
unset ANTHROPIC_API_KEY ANTHROPIC_MODEL ANTHROPIC_SMALL_FAST_MODEL
```

两条最容易踩的坑：

1. `BASE_URL` **不要带 `/v1`**——SDK 自己会拼 `/v1/messages`，写了会变成
   `/v1/v1/messages` 直接 404。
2. 那三个 unset **不能少**，见 §2.4。

差别只在于：手动配置是**静态**的（模型写死），而 newgate 注入档位名 + 代理
解析，所以 `--set-profile` 能对跑着的会话立刻改模型——这正是「语义命名层」
要解决的问题。
