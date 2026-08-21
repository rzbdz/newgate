# 12 · 前端簇（CLI / TUI / Web）与远程连接

> 需求 (5)：前端是一簇——CLI、TUI、Web 三种壳。Web 还要支持远程连接，类似 VSCode Remote SSH。

## 1. 三壳一核

```
        ┌──────────┐  ┌──────────┐  ┌──────────┐
        │   CLI    │  │   TUI    │  │   Web    │
        │ 一次性命令 │  │ 常驻交互  │  │ 富交互/远程│
        └────┬─────┘  └────┬─────┘  └────┬─────┘
             │             │             │ HTTP/SSE/WS
             └──────┬──────┘             │
                    ▼                    ▼
             ┌─────────────────────────────────┐
             │   Control Plane Core (共享)      │
             │  用例层：所有操作在这里定义一次    │
             └───────────────┬─────────────────┘
                             ▼
                        Config Store
```

**硬性规则：三个壳都不允许直接操作 Config Store 或自己实现业务逻辑。** 所有操作必须是 Core 里的一个 use case。

```ts
// packages/control/usecases.ts —— 唯一的真理
setDefault(tool, ref): Result
addProvider(spec): Result
probeProvider(id): ProbeResult
discoverTool(hint, opts): AsyncIterable<DiscoveryEvent>
diagnose(ctx): Findings
switchGroupBinding(group, role, target): Result
```

CLI 是「用 argv 调 use case，把结果渲染成文本」，TUI 是「用键盘调 use case，把结果渲染成 ANSI」，Web 是「用 HTTP 调 use case，把结果渲染成 DOM」。仅此而已。

这条规则是可维护性的命门。一旦允许某个壳「就这一个功能自己写一下」，三端行为漂移就开始了，而且不可逆——用户会发现「Web 上能删的 provider CLI 删不掉」，然后就不再信任任何一端。

### 能力对等矩阵

| 能力 | CLI | TUI | Web |
| --- | --- | --- | --- |
| 切换默认 provider | ✅ | ✅ | ✅ |
| Provider / Profile / Group CRUD | ✅ | ✅ | ✅ |
| 发现新工具 | ✅ | ✅ | ✅ |
| 诊断 | ✅ | ✅ | ✅ |
| 查看活跃会话、热切换 | ✅ | ✅ | ✅ |
| 用量图表 | 文本摘要 | 简图 | 完整图表 |
| 实时请求流 | `--follow` | ✅ | ✅ |
| **包装启动 tool** | ✅ 独占 | — | — |

只有「包装启动」是 CLI 独占——那是它的本职。其余功能三端对等。**没有「只有 Web 能干的事」**，这是对 cc-switch 这类 GUI-only 工具的结构性优势（见 [14-competitive-analysis.md](14-competitive-analysis.md)）。

## 2. TUI

### 为什么需要它

CLI 适合「我知道要干什么」，Web 适合「我想看看全貌」。中间有一大块场景两者都别扭：

- SSH 在服务器上，没有浏览器，但要在十几个 provider 里挑一个
- 想边看实时请求流边调整路由
- 不想记子命令，想按方向键浏览

而且 AI CLI 的用户本来就常年待在终端里。让他们为了切个模型去开浏览器，是一种打断。

### 布局

```
┌ newgate ─────────────────────────────────── ⏺ gateway  ◈ 3 sessions ─┐
│ [1]Dash [2]Providers [3]Tools [4]Groups [5]Sessions [6]Traffic [7]?│
├──────────────────────────────────────────────────────────────────────┤
│  Tool        Default Provider        Status                        │
│ ▸ claude       anthropic-official ✓    ready   · 1 session           │
│   codex        kimi              ✓     ready                         │
│   opencode     [group] cheap     ✓     ready   · 2 sessions          │
│   hermes       —                 ⚠     unverified descriptor         │
├──────────────────────────────────────────────────────────────────────┤
│ enter 切换  d 诊断  p 探活  g 编辑组  / 搜索  ? 帮助  q 退出           │
└──────────────────────────────────────────────────────────────────────┘
```

### 设计要点

- **一键切换是主路径**：方向键选中 tool → `enter` → 弹出 provider 列表（不兼容的置灰并写明原因）→ `enter` 确认。三次按键完成，无需离开当前视图。
- **实时**：订阅同一条 SSE 事件流，CLI 那边改了默认值，TUI 立刻更新。
- **Traffic 页**：gateway 启用时滚动显示请求（时间、tool、role、真实模型、token、耗时、状态）。这是排查「到底走了哪个 provider」最直观的方式。
- **降级**：非 TTY 环境（管道、CI）自动退回 CLI 模式，不要吐一屏 ANSI 转义。
- 尊重 `NO_COLOR`、`TERM=dumb`；提供无 Unicode 的 ASCII 回退。

## 3. Web

### 托管形态

```bash
newgate web            # 起 BE + 静态托管前端 + 开浏览器
newgate serve          # 只起 BE（远程/无头场景）
```

前端产物内嵌进二进制/包，**不依赖任何 CDN**（内网、离线、飞机上都能用）。

页面结构见 [07-control-plane-api.md](07-control-plane-api.md) §6，此处补充 Groups 页：

### Groups 页（多模型工具的主界面）

```
opencode · group: cheap                          [复制为新组] [设为默认]

 Role        Provider        Model                    来源
 primary     kimi        ▾   kimi-k2-0711-preview ▾   显式
 fast        kimi        ▾   moonshot-v1-8k       ▾   显式
 think       deepseek    ▾   deepseek-reasoner    ▾   显式
 search      local-vllm  ▾   qwen3-32b            ▾   显式
 title       (fallback)      → kimi / kimi-k2          继承 fallback
 vision      —               未绑定 · 发现时见于 /agent/vision/model
```

- role 列表由发现流水线自动枚举，不是手工维护。
- 每行独立下拉，可跨 provider 混搭。
- 「来源」列说明这个绑定从哪来（显式 / 继承 fallback / 模糊匹配），对应 provenance 原则。
- proxy 模式下改动**立即对运行中的会话生效**，页面上明确显示「已生效，无需重启」；非 proxy 模式则显示「下次启动生效」。这个区别必须在 UI 上说清楚。

## 4. 远程连接（类 VSCode Remote SSH）

### 4.1 场景

开发机在远端（云主机、公司内网、容器），AI CLI 跑在那边，但你想用本地浏览器管理它。

现在的做法是自己敲 `ssh -L 8787:127.0.0.1:8787`，能用但门槛高，而且端口冲突、多台机器切换都很烦。

### 4.2 架构

```
本地笔记本                               远端开发机
┌────────────────────────┐             ┌──────────────────────────┐
│ 浏览器                  │             │ newgate serve (BE)       │
│   ↕ http://127.0.0.1:X │             │   ↕                      │
│ newgate remote (客户端) │═══ SSH ════▶│ newgate-server (自动部署) │
│   本地端口转发           │   隧道       │   ↕                      │
└────────────────────────┘             │ Config Store / Gateway   │
                                       │ Tool 进程               │
                                       └──────────────────────────┘
```

```bash
newgate remote add prod --ssh user@host      # 记住一个远端
newgate remote open prod                     # 建隧道 + 开浏览器，一条命令
newgate remote ls                            # 已配置的远端 + 在线状态
newgate --remote prod use kimi --target claude   # 直接对远端执行命令，不开浏览器
```

### 4.3 关键设计（照抄 VSCode Remote 被验证过的做法）

**复用系统 ssh，不自己实现 SSH。**

理由：用户的 `~/.ssh/config` 里有 ProxyJump、跳板机、证书、`Include`、Match 规则、硬件密钥；他的 agent 里有已解锁的密钥；他的公司可能强制某种认证方式。自己实现 SSH 客户端不可能覆盖这些，只会制造无穷的兼容问题。

做法：`ssh -L <随机本地端口>:127.0.0.1:<远端端口> <host>`，主机名直接交给 ssh 解析。同时支持 `ProxyCommand`/`ControlMaster` 复用连接。

**自动部署远端组件。**

```
1. ssh 到远端，探测 uname/arch，检查 ~/.newgate/bin/newgate-server 是否存在且版本匹配
2. 不匹配 → 传输对应架构的静态二进制（或让远端自己下载，可配置）
3. 远端启动 server，监听 127.0.0.1 的随机端口，输出端口与 token
4. 本地建端口转发，打开浏览器
5. 断开时清理远端进程（或按配置保留，下次直接复用）
```

**远端只监听 loopback。** 隧道之外无法访问，即使远端机器有公网 IP。这一点不给任何配置项松绑。

**版本协商。** 本地客户端与远端 server 版本不一致时，API 可能不兼容。启动时握手比对，不兼容则自动更新远端组件（或明确报错）。

### 4.4 远程模式下的分工

| 事情 | 在哪执行 | 为什么 |
| --- | --- | --- |
| Config Store | 远端 | 配置属于 tool 运行的那台机器 |
| Tool 进程 | 远端 | 本职 |
| Gateway | 远端 | 必须与 tool 同机，否则延迟和网络暴露都不可接受 |
| 密钥 | 远端 | **绝不传到本地**；本地只看到脱敏引用 |
| UI 渲染 | 本地浏览器 | — |

**密钥不跨机传输**这条要守死。远程 UI 上编辑密钥时，输入的值经隧道直送远端存储，本地不落盘、不进 localStorage、不进浏览器历史。

### 4.5 多远端

一次可以连多台，UI 顶部有远端切换器（像 VSCode 左下角那个）。

更进一步：**聚合视图**——一屏看到 3 台机器上所有 tool 的当前 provider 和活跃会话。管理多台开发机的人会立刻理解这个价值。

### 4.6 非 SSH 的远端

- **容器**：`newgate remote add dev --docker <container>`，用 `docker exec` 替代 ssh，机制完全一样。
- **WSL**：`--wsl <distro>`。
- **裸 HTTP**：已有暴露的 BE（用户自己搭了隧道），`--url https://... --token ...`。**必须 HTTPS + token**，且明确警告风险。

## 5. 一致性保障

三端 + 远端，最大的风险是行为漂移。

| 措施 | 说明 |
| --- | --- |
| 单一 use case 层 | §1，最重要的一条 |
| 共享事件流 | 三端订阅同一条 SSE，任一端的变更立刻同步到其他端 |
| 契约测试 | 每个 use case 有一组用例，三端各跑一遍，断言结果一致 |
| 共享文案 | 错误码 → 人类可读消息的映射表共用，不各写各的 |
| 快照测试 | CLI 输出、TUI 渲染帧、Web 组件都做快照 |

## 6. 技术选型倾向（待定，见 roadmap）

| 部件 | 倾向 | 理由 |
| --- | --- | --- |
| CLI/TUI/BE | 同一语言同一二进制 | 一个可执行文件，远程部署时传一个文件搞定 |
| 分发 | 单文件静态二进制，无运行时依赖 | 远端部署、容器、老旧系统 |
| Web 前端 | 轻量 SPA，产物内嵌 | 不引 CDN，离线可用 |

**关于语言**：TS/Node 生态最贴近 tool 群体（大多是 Node CLI，L3 进程内劫持天然同源），但单文件分发和常驻内存不如 Rust/Go。Rust/Go 在分发和代理性能上更好，但 L3 hook 仍需要一份 JS。可能的折中：核心用 Rust/Go，L3 hook 单独出一个极小的 JS 文件。这个决定影响面很大，列在 [15-roadmap.md](15-roadmap.md) 开放问题里等你拍板。
