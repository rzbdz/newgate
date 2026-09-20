# 当前架构

## 1. 运行图

下面**只画参与排序的边**（`Need` / `Optional`）。往 ui 里的那批边也是排序边
（`Optional(cli)`，弱依赖，见 §4 与 `docs/02-component-framework.md`），但它们
方向一致、都从业务模块指向界面，所以单独列在下面。

```text
Config（无出边）
ConfigHook（无出边）
CLI（无出边）
GLM（无出边）
  ▲
  │ Optional(cli) ×9（弱依赖，排序边）：breaker / config / config-hook / gateway
  │            opencode-omo / plugin-manager / runtime / claudecode / tui
  │ 装了界面就注册命令与状态行；没装就跳过（`ok == false`），模块功能不受影响

Gateway
  ├─ Config
  ├─ Thinking / Claude Code behavior / cross-components
  └─ DeepSeek
       └─ plugin-manager

breaker
  └─ Gateway

Runtime
  ├─ Config
  └─ ConfigHook

PluginManager
  └─ ConfigHook

Wrapper
  ├─ Runtime
  └─ ConfigHook
```

**`breaker → Gateway` 这条边的方向是 2026-09-18 翻过来的**（那天之前是
`Gateway ├─ breaker`）。理由与判据见 §2：健康表有生命周期、有所有者，是
capability 不是库。翻过来之后 gateway 成了**最小系统**——它从自己的状态机里读出
四个决策点（建链准入 / 结局裁决 / 控制面自报 / 停机落盘），留一口注册表，谁都不
装也照常转发；健康表在**它自己的 `Start`** 里经 `RegisterFilter` 把自己挂进去。
可机械核对，**而且有测试钉住**（`modules/gateway/direction_test.go`）：那三处
（`forward/` 整个目录 + `module.go` + `serve.go`）的**非测试源码**里不出现
`breaker` 这个词，整个 `modules/gateway/` 也不 import 那个装逻辑的包。
**为什么这条法则需要一条测试而不是一句注释**：破了它，编译照样过、单测与 e2e
照样全绿——`breaker → gateway` 这条边还在，热路径里 import 一句就长回去了，而
行为一字不变；倒置之前的样子就是这样（`forward.Server` 上挂一个 Health 字段、
按 `BucketShape` 分支），依赖图上一点痕迹都没有，唯一的守卫是「有没有人去看」。
`…/modules/breaker/status` 不受判据 2 约束：它是 **wire 叶子**（§2 明确允许直接
import，优雅交接期间新旧二进制混跑，控制面文档的形状是跨版本契约）。

这也是「一个模块可以往 owner 的注册表里注册，而且 owner 在它之前启动」的第一个
真实案例，所以 §2 那条「需要被启动、被停止、被撤销吗」的判据之外，还有一条更常
被违反的规矩——**`Start` 只做两件事：起自己的服务、往 owner 注册自己的贡献；
它不得把「别人注册了什么」当作自己启动的输入**（见
`docs/02-component-framework.md` 的三段法则）。

箭头表示 capability 消费，不表示目录 import。实际启动顺序由 component Manager
根据 `Requires`/`Provides` 计算：拓扑序把 `cli` 排在所有注入者**之前**，停止是
逆序，于是注入者的 `Stop` 先把注册撤掉，界面最后停（`app/graph_test.go` 的
`TestInjectorsStartAfterTheUI` 守着这条）。

**没有 ui 时一切照常**：把 `modules/cli` 摘掉，整张图仍然装配成功，上面那 9 条
弱依赖因为**没有提供者而不建边**（各模块拿到 `ok == false` 就跳过），功能一个
不少，只是没有命令行入口。

### 注：发行版的模块不在本图里

本仓库只是 **core**：它提供机制，不提供产品定义。「这个二进制由哪些模块组成」
由发行版说了算——发行版是**另一个仓库、另一个 Go module**，它把 core 当依赖
（`replace` 到自己的 `core/` submodule），并把 `app.Selection` 交给内核的组合根：
内核自带的减去它要关掉的、加上它自己的（见 `docs/09-extension-guide.md` §8）。

对这张图来说发行版模块**没有任何特殊**：同一张依赖图、同一套 `Provides/Requires`、
同一套生命周期。区别只在**谁决定装**——`modules/` 按目录全装，发行版的按规格书
点名。内核**不认识它们中的任何一个**（连名字都不认识）：`app/direction_test.go`
守着组合根不 import 任何具体模块，`app/independence_test.go` 守着「内核里没有
发行版机制」这条边界。

所以读这张图时请注意：图中某些模块（上游怪癖修补、客户端×模型交叉语义）**不在
本仓库的 `modules/` 里**。那也是刻意的——它们随发行版变，不该钉死在 core。

## 2. 目录所有权

| 目录 | 所有权 |
| --- | --- |
| `component` | capability graph 和生命周期 |
| `modules/config` | domain、profile resolver、store、动态 role（后三者同时是**共享叶子**，见下面那条注） |
| `modules/confighook` | Agent、takeover、state field registry |
| `modules/gateway` | HTTP 数据面和扩展执行 |
| `modules/breaker` | binding 健康表（可用性 + 延迟排序）。**作为策略插进 gateway 的四个决策点**（`Need(gateway)`），不再被任何模块 import——这条边的方向 2026-09-18 翻过，先例与判据见 §1 |
| `modules/pluginmanager` | 运行期开关账本 + 模块分类词汇表 |
| `modules/tui` | menuconfig 风格终端界面（与 cli 同级的另一个 ui） |
| `modules/configshare` | 多机共享配置（预留，见 `docs/12-newgate-remote.md`） |
| `modules/runtime` | daemon、进程启动、shim、takeover |
| `modules/cli` | 参数分派、渲染排版、进程退出码；命令/诊断/状态行由各 owner 注入，**界面不认识任何模块** |
| `modules/<client>` | 客户端描述和客户端独有行为 |
| `modules/<model>` | 模型族识别和上游独有行为 |
| `modules/<client>_<model>` | 只存在于交叉点的行为 |
| `app`（在 `modules/` 之外） | 组合根：装配清单与 `App` 所有权对象 |

### 注：模块之间只认 capability，但**共享叶子**可以直接 import

上表里有些目录被别的模块直接 import（`config/paths`、`config/store`、
`config/domain`、`config/resolve`、`gateway/controlplane`、`breaker/status`、
`cli/extension`）。**那不是依赖边**，`Requires` 里看不见它们，棘轮测试也不管
——这是有意的，判据是「把它删掉，还剩下什么」：

- **共享叶子**是一段**无状态、无生命周期**的基础设施：文件的路径（paths）、
  配置的持久化与解析（store / domain / resolve）、控制面的读写与文档形状
  （controlplane）、wire 类型（breaker/status）、界面契约（cli/extension）。
  它们没有「谁拥有它」这回事，装几份都一样，所以谁都能引。
- **capability** 是**有生命周期、有所有者**的服务：健康表、网关端口、接管、
  客户端目录、插件账本。要它就得 `Need` / `Optional`，因为框架要按它排启动顺序、
  要在 Stop 时撤销。

判断标准只有一条：**这个东西需要被启动、被停止、被撤销吗？**需要，就是 capability；
不需要，就是共享叶子。2026-09-18 `quirk` 从包级全局改成随请求传递，正是因为它是
**有主的状态**（网关学到的上游事实），却走了一条共享叶子的形状。

`lib` 只容纳无状态、无注册、可跨组件复用的低层工具。

## 3. Owner API

Capability contract 属于提供能力的模块，写在 owner 根目录的 `api.go`（不建
`api/` 子包）。例如 Gateway hook contract 位于 `modules/gateway/api.go`，
Config role provider contract 位于 `modules/config/api.go`。Provider 和
consumer 都依赖 owner 的根包。

这样替换实现不改变 consumer，也不会产生全知的中心 contracts 包。

**跨模块贡献一律走 owner 的 `RegisterX() (Release, error)`。** 模块的 `Provides`
只放自己的 port；要往别人的扩展点插东西，就消费它的 service 再注册。注册是
运行期动作，有撤销、有查重、有生命周期；`Provide` 只是"这个端口归谁"的静态
声明。（2026-09-17 之前还有一条声明式的 `Provides: []Provision{Provide(别人的
capability, 值)}` 路径，已删除——它没有 Release、没有查重，两个模块认领同一个
命令名会静默先到先得。）owner 侧的账本用 `component.Registry[T]`，别手写。

契约类型若实现方需要反向引用，定义下沉到实现包、根 `api.go` 做类型别名转发：

| 契约 | 定义在 | 根 api.go 转发 |
| --- | --- | --- |
| Gateway `Request` / `Plugin` | `gateway/special`（插件层用它） | `type Request = special.Request` |
| Config `RoleProvider` | `config/roleprov`（注册表实现它） | `type RoleProvider = roleprov.RoleProvider` |

这样根包能 import 实现包（`gateway` → `gateway/special`），实现包不必回头
import 根包，循环依赖不会出现。

## 4. 数据与控制

磁盘配置是持久事实源。Watcher 将配置装成不可变快照，Gateway 热路径只读当前
快照。CLI 可以编辑配置、控制 daemon、查看诊断；Gateway 负责请求数据面。

Daemon 通过 pid/lock/state 文件和本地控制端点管理。升级时旧进程把 listener
移交给新进程并排空在途请求。

## 5. 当前兼容桥

部分旧数据面仍通过 package default registry 或 Runtime agent catalog 接入。
这些是明确的迁移边界，不是推荐的模块调用方式。新模块必须使用 capability 和
实例依赖，不得新增 service locator。
