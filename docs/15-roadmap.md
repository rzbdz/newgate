# 15 · 路线图与开放问题

## 1. 里程碑

原则：**每个里程碑独立可用**。不做「做完 M3 才有价值」的规划。

### M0 · 测试框架（第一件事）

全本地 mock，零 token，不出网。详见 [17-testing.md](17-testing.md)。

- [ ] **FakeUpstream**：三协议路由 + 请求记录 + 全部失败场景 + 可控时钟
- [ ] **FakeTool**：五种 `reads` 策略 + 不读 proxy env / 证书固定 / 自己覆写配置
- [ ] **MockFactory** + `teardown()` 自断言
- [ ] CI 阻断 egress，出网即失败
- [ ] argv 切分器的测试全部写好（实现可以后写）
- [ ] 一条贯通的 L4 骨架用例

**为什么排在最前**：FakeUpstream 同时是[发现探针](10-llm-native.md) §2 阶段 D 的实现——写它不是前置开销，是直接在写产品功能。

### M1 · 语义命名层跑通（目标：自己天天用）

范围极窄，只验证核心假设——**「维护 5 个名字」比「维护 M×N 处配置」好用**。
窄在覆盖面（一个工具、一种接管方式），深在一个点（流式必须完美）。

- [ ] 绑定表：`(tool, role) → (provider, model)`，人可读文本 + schema 校验
- [ ] **反向代理**：查表 → 改请求体 `model` 字段 → 转发。**同协议透传，不做翻译**
- [ ] 流式：SSE 逐块转发 + 三段超时（首字 60s / 静默 120s / 非流式 600s）
- [ ] `newgate init claude`：一次性 bootstrap，展示 diff 让用户确认，保留备份
- [ ] `newgate set heavy myplan/opus-5`：改绑定，运行中的会话下个请求即生效
- [ ] `newgate off`：一键恢复直连（逃生舱，与 init 同等优先级）
- [ ] **preset**：整套 role 绑定 + `"*"` 通配 + `extends`
- [ ] **wrapper**：argv 切分器（[03-cli-spec.md](03-cli-spec.md) §2 每条规则一个测试）+ argv0 分发 + `preset install`
- [ ] preset 走 URL 路径（`/p/<preset>`），代理无状态解析
- [ ] fail-open：绑定表读不出用内存旧表；role 查不到原样透传
- [ ] 常驻：用户级服务 + 自动重启 + `newgate doctor` 检测
- [ ] 决策回传头 `x-newgate-route`：本次实际走了谁
- [ ] SecretRef：`env:` / `file:`
- [ ] 只支持 claude 一个工具

**退出标准**：改一行绑定，正在跑的 claude 会话下一个请求就换了模型，无需重启；`newgate-toy claude` 与 `newgate-professional claude` 可同时在两个终端跑且互不干扰；`newgate off` 之后把 newgate 删掉，claude 照常工作。

> M1 **不含**：协议翻译、多工具、group、LLM、TUI/Web、MITM。
> 从现有配置**自动导入** provider 是冷启动生死线，放 M2 开头。

### M2 · 能扩（目标：不改代码就能接新工具）

- [ ] tool 描述符 schema + 用户自定义描述符加载
- [ ] `injection:config`（sandbox 优先，inplace 兜底 + 冲突处理 + 格式保真）
- [ ] `injection:composite`
- [ ] 内置 codex、opencode 描述符（走 [05-tools.md](05-tools.md) 的核实清单）
- [ ] **探针**（fake upstream + smoke command + 三态判定）—— 这是后面所有 LLM 能力的地基
- [ ] `newgate doctor`
- [ ] Role / Preset 数据模型 + `--group`
- [ ] provider 探活与能力矩阵

**退出标准**：写一份 JSON 就能接入一个新 CLI，且探针能实测确认它真的生效了。

### M3 · 能看（目标：BE + 三壳）

- [ ] BE：REST + SSE + 配置 watch + 鉴权
- [ ] Web：Dashboard / Providers / Tools / Groups / Sessions
- [ ] TUI
- [ ] use case 层 + 三端契约测试（[12-frontends.md](12-frontends.md) §1，这条不能拖到后面补）
- [ ] 配置历史与回滚

### M4 · 能代理（目标：热切换与观测）

- [ ] Gateway：L0 baseUrl 重定向 + 用量统计
- [ ] **L1 虚拟模型命名空间**（本项目最重要的差异化能力）
- [ ] anthropic ↔ openai 协议翻译（含流式与工具调用的 golden 测试）
- [ ] per-session 令牌与路由上下文
- [ ] 故障转移 + 熔断 + 重试红线
- [ ] 运行中热切换

### M5 · 能自适应（目标：LLM 原生）

- [ ] Evidence Bundle 采集（含二进制 strings 扫描 env/endpoint 候选）
- [ ] 发现流水线 A→E + 溯源标记
- [ ] 漂移检测与自动修复
- [ ] LLM 诊断（确定性规则先行）
- [ ] newgate as MCP server

### M6 · 能远程 / 能兜底

- [ ] `newgate remote`（SSH 隧道 + 自动部署 + 版本协商）
- [ ] 多远端聚合视图
- [ ] docker / WSL 远端
- [ ] **L3 进程内劫持**（Node/Bun/Deno）
- [ ] **L2 透明拦截**（受限 CA + 劫持名单 + 模糊匹配）

> M6 把 L2/L3 放最后，是因为它们价值高但风险也高（MITM 的信任成本）。等前面的基础稳了、用户已经信任这个工具了，再引入。

## 2. 需要你拍板的开放问题

### Q1 · 实现语言 ★ 影响最大

**在语义命名层的定位下，这个决定更清楚了**：核心是一个常驻的、必须极稳的小代理 + 一张表。

| 选项 | 优 | 劣 |
| --- | --- | --- |
| **Go** | 单文件、启动快、常驻内存小、并发模型天生适合代理、开发不慢 | 生态与 tool（多为 Node CLI）稍远 |
| **Rust** | 同上且更省资源；与 cc-switch 同级 | 开发慢，M1 想快的话是负担 |
| **TS / Node** | 生态贴近 tool；L3 hook 天生同源；迭代最快 | 常驻内存大、单文件依赖打包器——而**常驻稳定性是我们的命门**（[16 §6.2](16-core-thesis.md)） |

我的倾向变成**明确的 Go**：M1 就是"一个必须 7×24 稳定的小代理"，Go 在这个形态上的综合成本最低；L3 hook 那份极小的 JS 单独出文件即可。

### Q1b · wrapper 的定位 ✅ 已定

**保留，且进 M1。** 它的价值不是"少打字"，而是**per-invocation 覆盖不改变任何全局状态**——`newgate-free claude` 跑完，世界和跑之前一样，"切回来"不是一个动作。对照现有工具「每次切换都是全局写入、用完必须记得切回去、忘了也不会有提示」的模型，这是结构性差异。

配套确定：argv0 分发（`newgate-toy claude`）、`newgate preset install`、preset 编码进 URL 路径而非 session 令牌（无状态，代理重启不受影响）。见 [16-core-thesis.md](16-core-thesis.md) §3 与 [03-cli-spec.md](03-cli-spec.md) §1.5。

因此 **argv 切分器回到 M1**——透传规则（`newgate-toy claude --resume abc`）仍然必须正确。

### Q2 · "tool" 这个词对外用不用？

内部概念清晰，但对外可能显得攻击性强/不专业。备选：`target`、`tool`、`client`、`app`。
目前文档的做法是：内部叫 tool，CLI 参数叫 `--target`。要不要连内部也改？

### Q3 · 密钥默认后端

OS 钥匙串（安全，但 Linux 上 libsecret 依赖麻烦、SSH 无头环境不可用）vs 加密文件（可移植，但要设计主密码 UX）vs 默认 `env:`（最简单，把责任交给用户）。
考虑到远程/容器场景是我们的差异化，**默认可能应该是加密文件而非钥匙串**。

### Q4 · hermes 的信息

[05-tools.md](05-tools.md) §2.4 需要：配置文件路径与格式、认哪些 env、协议方言、内部有哪些模型槽位。
你如果手边有，直接给我；没有的话我们就靠发现流水线去探（这正好是它的第一个真实用例）。

### Q5 · M1 只支持 claude 够不够？

我倾向够——先把一个做到极致。但如果你的实际使用里 opencode/hermes 更高频，M1 的目标 tool 可以换。

### Q6 · 前端框架

React（生态大、shadcn 可用）vs Svelte/Solid（产物小，适合内嵌进二进制）。
考虑到要内嵌进单文件二进制且离线可用，**产物体积是个实际约束**。

### Q7 · 开源与许可

开源吗？什么许可？这影响能否参考 cc-switch（MIT）的预设数据，也影响社区共建 tool 描述符的可行性。

### Q8 · 名字

`newgate` 有点像 Nightingale/newgate prison。要不要考虑更能表意的名字？（不急，但改名越晚成本越高。）

## 3. 关键风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| tool 上游频繁变更配置格式 | 适配失效，用户流失 | 漂移检测 + 探针 + LLM 自修复（这正是 M5 的价值） |
| 探针做不出来（tool 无非交互模式） | M5 的地基塌了 | M2 就验证探针可行性，别等到 M5；做不出来的 tool 退回人工适配 |
| L2 MITM 引发用户信任危机 | 口碑受损 | 默认关闭、首次强提示、CA 不进系统信任库、劫持名单可审计 |
| 启动开销超标 | 核心体验崩塌，用户直接回去用原命令 | M1 就设性能测试门禁（CI 里跑，超过 50ms 就 fail） |
| 概念过多劝退新用户 | 获客失败 | 默认路径零概念（[13-pain-points.md](13-pain-points.md) 反痛点） |
| 竞品补上 CLI | 时间窗关闭 | M1 极窄，尽快发布 |
| LLM 生成错误适配导致用错 provider | 信任崩塌（账单/质量问题） | 探针裁决 + 置信度标记 + 绝不假装成功 |

## 4. 下一步

文档层面还缺：

- [ ] `docs/00-getting-started.md`（面向新用户的两概念版本，与设计文档分离）
- [ ] `docs/16-testing.md`（把散在各文档的测试要求汇总成矩阵）
- [ ] 一份可运行的 tool 描述符样例文件 + JSON Schema

但在写更多文档之前，**建议先回答 Q1（语言）和 Q5（M1 目标 tool），然后直接开始写 M1 的 argv 切分器**——那是整个项目里规则最多、最适合用测试驱动、也最能验证设计是否想清楚的一块。
