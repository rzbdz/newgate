# 07 · 控制面 API（BE ↔ FE）

## 1. 定位

BE 是 Config Store 的**编辑器 + 观测端**，不是运行时的必经之路（见 [02-architecture.md](02-architecture.md) §1）。它掉线时 CLI 照常工作，只是 Web 看不到东西。

职责：

- 读写配置（带校验，避免用户在 Web 上写出 CLI 读不了的东西）
- provider 探活与延迟测量
- 汇总活跃 session
- 推送变更事件给 FE
- 代理 gateway 的统计数据

## 2. 传输与鉴权

- 默认只监听 `127.0.0.1:8787`。**不提供 0.0.0.0 的便捷开关**；想远程访问自己开 SSH 隧道。
- 启动时生成随机 token，写入 `~/.local/state/newgate/daemon.json`（0600）。CLI 从这里读，FE 通过启动时的一次性 URL 参数拿到并存 sessionStorage。
- 每个请求带 `Authorization: Bearer <token>`。
- 校验 `Origin`：只接受 newgate 自己的前端来源，防止浏览器里其他页面发跨站请求打本地端口（DNS rebinding / CSRF）。
- 写操作要求 `Content-Type: application/json`（排除简单表单请求）。

## 3. REST API

统一响应包络：

```jsonc
// 成功
{ "ok": true, "data": … }
// 失败
{ "ok": false, "error": { "code": "PROVIDER_NOT_FOUND", "message": "…", "details": {…} } }
```

### Providers

| Method | Path | 说明 |
| --- | --- | --- |
| GET | `/api/providers` | 列表（credential 一律返回引用字符串，不返回明文） |
| POST | `/api/providers` | 新建 |
| GET | `/api/providers/:id` | 详情 |
| PATCH | `/api/providers/:id` | 修改 |
| DELETE | `/api/providers/:id` | 删除（被 defaults 引用时返回 409 并列出引用者） |
| POST | `/api/providers/:id/test` | 探活：发一次最小请求，返回 `{ok, latencyMs, statusCode, modelsSeen?}` |

**探活**要区分「网络不通」「401 鉴权失败」「404 路径错」「200 正常」，并把这个区分呈现给用户——这是 provider 配置最高频的失败点。

### Tools / Profiles / Defaults

| Method | Path | 说明 |
| --- | --- | --- |
| GET | `/api/tools` | 内置 + 用户描述符，含「是否在 PATH 中找到」「版本」 |
| GET | `/api/tools/:id` | 详情，含可用注入方式的 probe 结果 |
| PUT | `/api/tools/:id` | 覆盖/新增用户描述符（校验 schema） |
| GET | `/api/profiles` · POST · PATCH · DELETE | profile CRUD |
| GET | `/api/defaults` | `{ byTool: {...}, global: "..." }` |
| PUT | `/api/defaults/:tool` | **设置某 tool 的默认 provider**（等价于 `--set-provider --target`） |
| DELETE | `/api/defaults/:tool` | 回落到全局默认 |

### Sessions

| Method | Path | 说明 |
| --- | --- | --- |
| GET | `/api/sessions` | 活跃 session：tool、provider、pid、启动时间、注入方式 |
| DELETE | `/api/sessions/:id` | 结束会话（发 SIGTERM，需二次确认标志 `?force=`） |
| POST | `/api/sessions/:id/switch` | **热切换 provider**——仅 `injection:proxy` 下可用，其他注入方式返回 409 并说明原因 |

### 其他

| Method | Path | 说明 |
| --- | --- | --- |
| GET | `/api/status` | 版本、配置路径、gateway 状态、孤儿 journal 数 |
| POST | `/api/doctor` | 触发一次体检，返回结构化结果 |
| GET | `/api/gateway/stats` | 用量/成本/错误率（gateway 启用时） |
| GET | `/api/schema` | JSON Schema，给 FE 做表单生成 |

## 4. 事件流

`GET /api/events`（SSE）。FE 订阅后不再轮询。

```
event: config.changed
data: {"kind":"providers","revision":42}

event: session.started
data: {"id":"01J…","tool":"claude","provider":"kimi","injection":"env"}

event: session.exited
data: {"id":"01J…","exitCode":0,"durationMs":183422}

event: provider.probe
data: {"id":"kimi","ok":true,"latencyMs":312}
```

**配置变更也可能来自 CLI**（用户在终端敲了 `--set-provider`）。BE 要 watch 配置目录（fs watch + 轮询兜底），把外部变更也广播出去。否则 Web 和 CLI 会显示不一致——这是双入口控制面最容易出的问题。

## 5. 乐观并发

配置资源带 `revision`（单调递增）。PATCH 需带 `If-Match: <revision>`，不匹配返回 409 + 当前值，FE 提示冲突。防止 Web 上编辑到一半、CLI 改了同一份配置导致覆盖。

## 6. FE 页面结构

| 页面 | 内容 |
| --- | --- |
| **Dashboard** | 每个 tool 一张卡片：当前默认 provider（可下拉直接切换）、可执行文件是否就绪、活跃 session 数 |
| **Providers** | 列表 + 增删改 + 一键探活；显示 endpoint 协议标签，让用户看清「这个 provider 能喂给哪些 tool」 |
| **Tools** | 描述符详情、注入方式 probe 结果、`doctor` 输出 |
| **Sessions** | 活跃会话，proxy 模式下可热切换 |
| **Gateway** | 开关、路由策略、用量与成本图表、最近请求（脱敏） |
| **Settings** | 密钥后端、日志级别、导入导出 |

### 关键交互

Dashboard 上「给 claude 换默认 provider」必须是**一次下拉选择**就完成，不需要进详情页。这是这个产品最高频的操作，任何多余的点击都是设计失败。

下拉里要**过滤掉协议不兼容的 provider**（或置灰并说明原因「无 anthropic 协议入口，需启用 gateway 翻译」），而不是让用户选完了才报错。

## 7. 前端技术取向

- 单页应用，构建产物由 BE 静态托管（`newgate web` 一条命令起服务并开浏览器）。
- 不做用户系统、不做多租户。这是本机工具。
- 离线可用，不引 CDN 资源（用户可能在内网）。
- 具体框架待定，见 [15-roadmap.md](15-roadmap.md) 开放问题。
