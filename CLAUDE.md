# CLAUDE.md — 在这个仓库里怎么干活

这是 **newgate 的内核**：组件框架（typed capability、依赖图、生命周期）、进程
入口契约、i18n 引擎、生成器与 lint 工具。**它一个模块都不带**（2026-11-18 起）。

> 网关、熔断、接管、界面、配置、客户端接入、上游怪癖修补——**全部**在发行版
> （`newgate-ext`）里。分法就是 Linux 内核与用户态：内核只给机制，产品由发行版
> 组装。判据一句话：**换个发行版，这东西还该在吗？**

架构见 `docs/03-architecture.md`。改代码前先读它——这个仓库的注释密度很高，
写的都是「为什么」。

**产品文档不在本仓库**（2026-11-18 搬去发行版 `docs/`）：配置、网关、推理、客户端
接管、运维、排障都在 `newgate-ext/docs/`。内核这一侧只剩框架、扩展、测试安全、
本地化四篇。

---

## 0. 三条先知道的（都踩过）

1. **先确认哪份 checkout 是线上**。跑着的 daemon 用的是
   `/root/workspace/newgate-ponyfish`（也就是**主开发 worktree，分支 `main`**），
   不是 `/root/workspace/newgate`（停在 `deprecated` 的旧快照）。
   改错那份 = 改完编译出来和线上行为对不上。
   判断线上版本：`go version -m /root/.local/bin/newgate | grep vcs.revision`，
   或看日志里那行 `newgate <版本> … 启动`。

   **分支流（2026-09-18 起）**：`main` 是唯一主分支，本地与 `origin/main` 同源。
   开发从 `main` 切分支 → 完事合回 `main`。曾经的主分支 `clean-tree` 已删除
   （2026-09-18，`c3bd2ed` 已整体并入 `main`）。`/root/workspace/newgate` 那个
   worktree 停在 `deprecated`（= 旧 main 血统的最后一版），只作历史留存，别在上面改。
2. **本机 shell 的两个坑**：`grep` 是 ugrep 包装、会静默吞结果 → 一律写
   `command grep`；`/bin/sh` 是 dash，没有 `exec -a`，测试里要「argv0 是特定
   名字」得用 `/bin/bash -c "exec -a …"`。
3. **你自己的请求也穿行在这个 daemon 里**。root 的 claude 被 PATH shim 接管，
   `newgate stop` 和 `newgate start` **绝不能拆成两个工具调用**——中间那个请求
   会撞上死掉的代理，会话当场挂。一切重启都放**一条命令**里（见 §3）。

---

## 1. 开发

```bash
# 仓库根就是模块根（2026-09-20 摊平了 go/ 那层）
make build      # 本机二进制 → bin/newgate（dynamic，本机够用）
make static     # CGO_ENABLED=0 全静态，跨机器部署必须用这个
make test       # == go test ./...
go vet ./... && gofmt -l .
```

分层（依赖通过 capability 表达，`component` 不 import 任何模块——它一个都不认）：

| 目录 | 管什么 | 改这里当你在做… |
| --- | --- | --- |
| `component` | typed capability、依赖 DAG、生命周期（强依赖 `Need` / 弱依赖 `Optional`，单阶段装配）、装配图（`Plan`）/启动（`Manager`）的分离、trace 出口 | 组件框架本身 |
| `component/entry` | **进程入口契约**：`Process` / `Handler` / `Registry` / `MakeProcess`。组合根与模块两侧唯一共用的词 | 入口类型、argv0 归一、rank |
| `lib/i18n` | 本地化引擎：账本、目录、`T` / `N` / `E`、`Extend` | 翻译机制 |
| `app` | 组合根逻辑：`Main`（trace → 起图 → 问入口）、`Selection`（由消费者决定装什么）、`CoreModules()`（**空的**） | 换装图方式、退出码 |
| `tools/genmodules` | 构建期扫描 `modules/`，生成装配清单（**只看本仓库、完全离线**）；本仓库扫出来是空的 | 改模块发现规则 |
| `tools/genmodules/scan` | 「哪些目录算组件」+「目录名怎么拧成 import 别名」的唯一实现（发行版生成器用同一份） | 改组件判据 |
| `tools/i18n` | 账本提取、校验、bundle、翻译（`sync` 只在本地跑） | 改本地化工具 |
| `tools/ciyaml` | 校验 GitHub workflow 的结构（两个仓库共用一条判据） | 改 CI 棘轮 |

**`core/modules/` 是空的，而且必须空着。** 一个模块该不该在这里，判据是
「换个发行版，这东西还该在吗？」——网关、熔断、界面、配置、客户端接入在答案里都是
「不该」，所以它们住在发行版的 `modules/`。`app/core_test.go` 的
`TestKernelShipsNoModules` 在有人把模块放回来时当场变红。

**模块契约（给发行版的模块用，本仓库只是它的定义处）**：一个目录、一个
`module.go`、导出 `func New() component.Component`，声明 `Requires`、`Provides`、
`Start`、`Stop`；生命周期只有这两个阶段（没有 `Attach`，没有「注入阶段」，见 §4）。
注册型 capability 必须返回 `component.Release`，consumer 在 `Stop` 中逆序释放。
目录规则（根 `module.go`、契约在根 `api.go`）由发行版的 `testing/kernel` 断言。

**公开 capability 和接口写在模块根目录的 `api.go`**（2026-09-17 起，不再用
`<module>/api/` 子包）；禁止建立中心化 contracts 包。契约类型若实现方需要反向
引用，定义下沉到实现包、根 `api.go` 做类型别名转发（见 `docs/03-architecture.md` §3）。

**`Provides` 只放自己的 service。** 跨模块贡献一律走 owner service 上的
`RegisterX() (Release, error)`；别做「既 provide 自己的 service、又 provide 别人的
service」。端口不声明基数（没有 `One`/`Many`），需要唯一性由 owner 自己在注册
逻辑里保证。

**装配清单是构建期生成的**（`app/modules_gen.go`，由 `tools/genmodules` 扫描
`modules/` 得到）：装一个模块 = 把目录复制进 `modules/`、重新编译。`make build`
会自动重生成，`make check-generate` 只校验；`app` 里有一条测试跑它，拦住
「加了模块忘了生成」的静默漏装配。

### 发行版：另一个 Go module（2026-09-20 起，2026-11-18 完整落地）

**core 只是内核**：组件框架、入口契约、i18n 引擎、工具。**模块全在发行版**——那是
**另一个仓库、另一个 Go module**，它把 core 当依赖（`replace` 到自己的 `core/`
submodule），并把自己那张表交给内核的组合根：

```go
app.Main(ctx, app.Options{
    Loader: app.Selection{Extra: []app.Entry{
        {Dir: "entry",    Component: entry.New()},
        {Dir: "gateway",  Component: gateway.New()},
        {Dir: "deepseek", Component: deepseek.New()},
    }},
    Trace: func() func() { return assembly.TraceToLogFile(paths.LogFile()) },
})
```

装哪些模块**不改 core 的代码**，core 也不认识任何模块名（连 `gateway` 这个词都不该有）。

**两条接缝**（2026-11-18）：

- `app.Selection{AllCore, Disable, Extra}`：消费者的模块表。`AllCore` 与 `Disable`
  今天对内核对空（内核没有模块），但语义保留着——第三方组合根如果自己往
  `CoreModules()` 里放东西，规则照样生效。**本仓库的规格书已经不用 `Disable`。**
- `app.Options.Trace` / `OnAssemblyFailure`：装配记录与失败的落点。内核**不再**
  认识发行版的日志路径——那段逻辑（指纹、去重、写文件）在发行版的 `lib/assembly`。

**2026-09-20 之前**内核根挂着一份 `modules-ext.json`（Pin），构建期 clone 发行版再扫
一遍；那套删了。**2026-11-18 之前**内核还自带十三个模块（gateway、cli、config…），
那时「内核 vs 发行版」只是仓库边界，模块仍住在核心里。现在这条线画在**模块身上**：

| 留在 `core/` | 搬去发行版 |
| --- | --- |
| `component`、`component/entry`、`app`、`lib/i18n`、`tools/` | **全部 `modules/`**、`lib/{httpx,style,view,logx,porthub,serving,buildinfo,durarg}` |
| `mock/fake_upstream.py`（按路径复用） | `testing/{testkit,upstream,system}`、`mock/` 的端到端 |
| 框架文档（`docs/02,03,09,10,13`） | 产品文档（`docs/01,04,05,06,07,08,11,12`） |

内核因此**完全离线**，而且连「真起一张图」都做不到——它没有模块：

```bash
make check                    # fmt / vet / 清单 / 单测（无 e2e：没有东西可服务）
GOPROXY=off go test ./...     # 离线也全过——「不需要网络」是事实不是承诺
```

`app/independence_test.go` 守着「内核不 import 发行版」；`app/core_test.go` 守着
「内核不带模块」；`app/selection_test.go` 用一张换进来的假表守住 Selection 的规则
（名字必须点得中、组合根自己的端口关不掉、AllCore 跟着表长）。

- 官方发行版：`git@github.com:rzbdz/newgate-ext.git`，**只有 `main` 一条分支**。
  **fork 它 + `build/build.sh` 就是一个新发行版**；`dist-hello.json` 是活的最小样例
  （`i18n` + `entry` + `hello`）。
- **两个仓库在同一个工作区里改**（2026-09-20 定）：开发只开
  `/root/workspace/newgate-ext`。改模块就在仓库根下改、直接提交推送；改内核进
  `core/`（submodule，改之前先 `git checkout main`），推完回发行版 `git add core`
  挪 gitlink。**不要为内核另开一个 checkout**。
- 测试边界（2026-11-18 之后）：**内核的测试只管框架**（component、entry、app、
  i18n、tools）。模块行为全在发行版：`testing/kernel`（整图不变式）、
  `testing/system`（整图 + 真转发）、`modules/*/_test.go`、`mock/*.sh`。

档位阶梯（2026-09-16 四档化）：
`heavy`(fable) > `normal`(opus，**主力**) > `mid`(sonnet) > `light`(haiku)，
外加正交的 `vision`。没写 `normal` 的 profile 由 `domain.CandidatesFor` 用
`mid` 顶上。改档位相关的东西，先看 `docs/04-configuration.md`。

---

## 2. 测试与验证

- **四层粒度，谁都替不了谁**（`docs/10-testing-security.md` §1.1）。原则是
  **测试跟着「谁知道这件事」走**：基础通用、不常改的功能（档位解析、字节手术、
  转移、流式）做**大测试**；各模块自己的特殊行为放**模块内部**，只有它自己清楚
  边界在哪：
  - 单元：纯函数，不需要额外设施；
  - 模块：`testing/testkit` —— `testkit.Start(t, 我, 桩...)` 装出「我 + 我的
    依赖链」的真组件图，断言接口通过性和注册/撤销对称性；`testkit.Sandbox(t)`
    圈住 `NEWGATE_HOME`/`NEWGATE_TARGET_DIR`/`HOME`；
  - 系统：`testing/system` —— `system.Start(t)` 起**整张**真组件图 + 真转发
    服务（**临时端口，不是 8899**，所以能和跑着的线上 daemon 并存）+ 进程内假
    上游（`testing/upstream`，两方言 + 流式 + 严格 reasoning + 故障注入）。
    不是 e2e：没有子进程、没有 PATH shim、没有真接管；
  - 端到端：`mock/*.sh` —— 真二进制、真进程、真接管。**刻意与 Go 侧解耦**
    （不 import 任何 core 包），所以 Go 怎么重构都不该影响它。它一红就是行为
    真的变了。
- **一把梭**：`make check`（格式 + vet + 生成清单 + i18n + 单测 + 零 token 端到端）。
  拆开：`make test` / `make test-race` / `make e2e`。
- **推之前自动跑**：仓库带了一份 `.githooks/pre-push`，内容就是 `make ci`（沙箱
  `NEWGATE_HOME` + 静态 / 单测 / 竞态 / 端到端，约半分钟）。装上——每个 clone 一次，
  git 不会替你装：

  ```bash
  git config core.hooksPath .githooks
  ```

  为什么要它：CI 只在推上去之后才说话，而 2026-09-20 一天里撞了两次「推完才发现」
  （workflow 文件非法 → GitHub 0 秒拒掉整个 run；发行版那边 gitlink 指着没推的提交
  → checkout 整体失败）。绕开用 `git push --no-verify`——这个口子**故意留着**：
  一条绕不过的钩子，在它自己出毛病的时候会把人卡死。
- **单测不出网**（`docs/10-testing-security.md` §1）：一律用 `httptest`，出网即失败。
- **端到端零 token**：`make e2e`（假上游 + 沙箱 `NEWGATE_HOME`，不碰真实配置）。
  改网关/插件行为后跑一遍，它锁的正是真实现场复现出来的那几条。**只有这一条**了
  ——opencode 侧那条跟着客户端模块搬去了发行版（那边的 `mock/e2e_opencode.sh`）。
- **打真实上游验证**（几个 token）：利用 `/p/<profile>` 的**单次 profile
  覆盖**，不用切全局状态：
  ```bash
  curl -s -X POST http://127.0.0.1:8899/p/ds/v1/messages \
    -H 'Content-Type: application/json' \
    -d '{"model":"normal","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}'
  ```
  两种方言各试一次（`/v1/messages` = anthropic 方言，`/v1/chat/completions`
  = openai 方言），然后看日志确认 `X-Newgate-Route` / `-> provider/model`。
- **`newgate probe` 有盲区**：主探活只打 provider **声明协议**那条路。
  「probe 全绿、真实流量 404」通常是方言/base 不对（见 `docs/05-gateway.md` 的
  `anthropic_url`）。配了双 base 之后 probe 会把两种方言分别打一遍。
- **不静默**是硬要求：任何改写都要有 `notes` / 日志行，测试里断言它。

---

## 3. 部署

**这个仓库不产出产品二进制。** 内核没有模块，`make build` 编出来的 `bin/newgate`
起来之后没有任何入口（它只会说「no entry claimed this call」）——它不是降级版，
是**一个框架**。

产品和它的部署（换二进制、优雅交接、跨机器、重新接管配置）全在发行版：
`newgate-ext` 的 `docs/08-operations.md` 与它的 `CLAUDE.md` §3。

## 4. 代码约定

These rules are the kernel's (they apply to the framework code in this repo).
The product-specific conventions — byte surgery on request bodies, the data-plane
policy port, upstream-patch plugins, UI injection ports, module key registration —
now live with their modules in the distribution; see `newgate-ext/CLAUDE.md` §2.

- **不静默**：任何失败、任何改写、任何「我跳过了」都要有一条可读的回报。这条是
  框架级规矩（`component` 的 trace、`app` 的装配记录、`Selection` 的每一条报错
  都是它的落地），不是某个产品的偏好。
- **fail-open**：框架自己的辅助路径（trace、catalog 交付、面板）出错不该让整条
  链断掉。`Start` 失败仍然要回滚（那是装配错误），但**诊断输出**永远不能成为
  新的故障源。
- **注释写「为什么」**：现场报错原文、实测数字、日期、以及「为什么不是另一
  种做法」。这是这个仓库最重要的资产。
- **文档同步**：动了框架行为就改 `docs/`（`00-index.md` 有全表）。新机制写进
  `docs/09-extension-guide.md`。
- **模块契约不认模块名**：内核提供类型（`Capability`、`Requirement`、
  `Provision`、`Entry`），产品填内容。`component.Type` 的注释里那条规矩在两个
  仓库都成立：core 提供表，产品填内容。
- **契约的观测口是对外 API**：`Requirement.Name()` / `Optional()`、
  `Provision.Name()`、`Plan.Dependencies()` / `AllProviders()` 这些访问器存在
  的唯一理由是**产品层能写自己的不变量测试**（「业务模块对 ui 只许 Optional」
  这类），而内核不解释那些概念。新增不变量时优先加访问器，别把产品概念塞进内核。
- **提交**：英文 subject（`fix(scope): …` / `feat(scope): …`），正文说明
  「现场是什么样、为什么这么改、验证了什么」。概念的设计动机写在所属
  package 或声明旁；行为变化才更新专题文档。结尾只带
  `Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>`。
- **用户可见的文本一律走目录表**（2026-09-20 起）。源码里写**英文**：
  `i18n.T("unknown command {cmd}", i18n.A{"cmd": args[0]})`；中文、以及将来任何语言，
  都活在 `lib/i18n/catalogs/<tag>.json` 里。判据一句话——**非测试 Go 源码里不许出现
  中文字符串字面量**（注释不算：注释是设计记录，本来就该是中文）。`app/i18n_test.go`
  与 `make check-i18n` 守着它；迁移期的豁免列在 `tools/i18n/allowlist.json` 里，
  **只减不增**，终点是空文件。细则：
  - **键就是那句英文**（gettext 的 msgid 传统）：没有「消息 ID」这层间接，也就没有
    「键写错了」这回事。代价是改措辞会让旧译文变孤儿——`tools/i18n check` 会报出来。
  - **机器标记留在消息外面**：`[proxy]`/`[shape-400]` 这类 tag、metrics 计数器名、
    `ok/warn/bad/skip`、落盘 JSON 字段名、命令名、flag 名、路径。它们是 grep 的锚点
    与协议的字段，翻译它们等于改协议。
  - **占位符用具名** `{dir}`（不是 `%s`）：译文要能重排语序。带数量的句子用
    `i18n.N("1 file", "{n} files", n, …)`——键用**单数**形式，中文只写 `other`。
  - **错误**用 `i18n.E(...)` / `i18n.Ef(err, ...)`；测试断言用 `i18n.ID(err)`
    （语言无关的身份），别断言渲染出来的那句话。
  - 写完新文案跑 `go run ./tools/i18n extract` 重建账本；`make check-i18n` 会告诉你
    还缺什么。补译文用 `make i18n-sync`——**它调本机网关跑 LLM，只在开发者本地跑，
    绝不进 CI**（CI 只跑不花钱的 `check`）。机翻条目标着 `machine`，人工复核后改成
    `reviewed`；发版前 `check -strict` 会拦住没复核的。
  - 语言解析次序（`NEWGATE_LANG` > 配置 > `LC_ALL`/`LC_MESSAGES`/`LANG` > 源语言）
    由产品层决定，实现在发行版的 `modules/locale/resolve.go`；内核这一侧只提供
    `lib/i18n.Install` / `Extend` 这两个装配口。

---

## 5. 排查内核

内核自己只有两条排查面：

```bash
NEWGATE_TRACE=1 <二进制> …     # 装配过程逐行打到 stderr（不缓冲、不判重）
```

- 装配记录（谁装了、什么顺序、哪条弱依赖没命中）由 **组合根经
  `component.SetTrace` 的出口**产出——内核只提供事实，写去哪由产品决定。
- 内核的每条报错都点名**端口**，不是模块名（内核不认识模块）。看到
  `component X requires missing capability Y` 时，缺的是端口 Y——哪个模块该提供它，
  由产品的 `Selection` 和它的依赖图回答。

产品侧排查（网关日志、dump、`newgate doctor`…）见发行版 `docs/11-troubleshooting.md`。
