# 00 · 文档索引与状态

## 阅读顺序

新读者按这个顺序，两篇就够形成完整判断：

| 顺序 | 文档 | 为什么先读 |
| --- | --- | --- |
| 1 | [16-core-thesis.md](16-core-thesis.md) | **核心论点**。产品是什么、删掉了什么、代价是什么 |
| 2 | [14-competitive-analysis.md](14-competitive-analysis.md) | **为什么不用现成的**。cc-switch / OmniRoute 的代码级分析 |
| 3 | [15-roadmap.md](15-roadmap.md) | 里程碑与**待拍板问题** |
| 4 | [13-pain-points.md](13-pain-points.md) | 痛点全景与优先级 |

之后按需查设计细节（下表）。

## 设计经过两次转向

诚实记录，避免读者被旧文档误导：

| 阶段 | 核心假设 | 产物 |
| --- | --- | --- |
| **v1** 注入优先 | 每次运行都改配置/注入 env，退出时还原 | 01–09 |
| **v2** full proxy | 配置只写一次，之后运行时路由 | 11、16 首版 |
| **v3** 语义命名层（当前） | 本质是**命名间接层**问题；简洁本身是差异化 | 16 现版、14、README |

**v3 是当前论点。** v1/v2 的文档没有删除——它们的机制细节（注入接口、协议翻译、崩溃恢复）在 L2/L3 兜底路径和后续里程碑里仍然要用，只是不再是主路径。受影响的文档顶部有标注。

## 文档状态

| 文档 | 状态 | 说明 |
| --- | --- | --- |
| [00-index.md](00-index.md) | ✅ 当前 | 本文件 |
| [01-concepts.md](01-concepts.md) | ⚠️ 部分过时 | 概念与类型定义有效；解析优先级偏 v1。Preset 权威定义见 16 §3 |
| [02-architecture.md](02-architecture.md) | ⚠️ 部分过时 | 分层与模块边界有效；「CLI 直读配置、不依赖 daemon」是 v1 结论，v3 下代理必须常驻 |
| [03-cli-spec.md](03-cli-spec.md) | ✅ 当前 | argv 切分规则、argv0 分发、透传逐例验证表。**M1 直接照此写测试** |
| [04-injection.md](04-injection.md) | ⚠️ 已降级 | 注入 → bootstrap（一次性）。顶部有说明。inplace 冲突/崩溃恢复只在 L2/L3 用 |
| [05-tools.md](05-tools.md) | ✅ 当前 | 工具描述符规范 + 各 CLI 适配草案 + **核实清单** |
| [06-config-schema.md](06-config-schema.md) | ⚠️ 部分过时 | 磁盘布局、密钥引用有效；需补 presets/bindings 的 schema |
| [07-control-plane-api.md](07-control-plane-api.md) | 🔜 后置 | M3 才用到 |
| [08-gateway.md](08-gateway.md) | ⚠️ 范围收窄 | 路由/观测有效；**协议翻译 M1 不做**（同协议透传） |
| [09-security.md](09-security.md) | ✅ 当前 | v3 下分量更重（代理持有全部凭证） |
| [10-llm-native.md](10-llm-native.md) | ✅ 当前 | 发现流水线、漂移检测、诊断。定位是**降低接入成本**，非核心 |
| [11-proxy-native.md](11-proxy-native.md) | ✅ 当前 | 四级接管 L0/L1/L2/L3。L1 是主路径 |
| [12-frontends.md](12-frontends.md) | 🔜 后置 | M3/M6 才用到 |
| [13-pain-points.md](13-pain-points.md) | ✅ 当前 | |
| [14-competitive-analysis.md](14-competitive-analysis.md) | ✅ 当前 | 代码级，带 `文件:行号`。末尾有**复核清单** |
| [15-roadmap.md](15-roadmap.md) | ✅ 当前 | |
| [16-core-thesis.md](16-core-thesis.md) | ✅ 当前 | **权威文档**，与其他文档冲突时以此为准 |
| [17-testing.md](17-testing.md) | ✅ 当前 | **M0**：全本地 mock、零 token。FakeUpstream 同时是发现探针 |
| [18-role-preference.md](18-role-preference.md) | 📐 设计中 | **Profile 链**：priority + 绑定 list + pinned/excluded + mood。**待 review** |

## 术语约定

已统一，写代码时照此：

| 用这个 | 不要用 | 说明 |
| --- | --- | --- |
| `tool` | victim、target、app、client | 被接管的 CLI。~~早期用 victim~~，已全量改为 tool |
| `role` | slot、tier、档位 | 语义标签：`heavy` / `cheap` / `fast` / `search` |
| `binding` | mapping | `(tool, role) → (provider, model)` |
| `preset` | group、ModelGroup | 一整套 binding。~~早期叫 ModelGroup~~，已统一 |
| `provider` | vendor、backend、account | |
| `bootstrap` | install、注入 | 一次性把 tool 配置指向代理 |
| `passthrough args` | child args、rest args | |

`--target` 保留为 `--tool` 的别名（兼容最初需求里的 `--set-provider bb --target hermes` 写法）。

## 当前状态

**设计阶段，无代码。**

下一步：**M0 测试框架**（[17-testing.md](17-testing.md)）。全本地 mock、零 token、不出网；FakeUpstream 同时就是发现探针的实现，所以这不是前置开销。

同时需要定 [15-roadmap.md](15-roadmap.md) §2 的 Q1（实现语言）——它决定 M0 用什么写。
