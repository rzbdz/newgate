# CLAUDE.md — 在这个仓库里怎么干活

newgate：把 AI CLI（claude / opencode）的模型选择收敛到**语义档位层**，网关侧
做路由、fallback、上游怪癖修补。设计思想见 `docs/01-product.md`，架构见
`docs/03-architecture.md`。**改代码前先读这两篇**——这个仓库的注释和文档密度
很高，而且写的都是「为什么」，不是「是什么」。

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
2. **本机 shell 的三个坑**：`grep` 是 ugrep 包装、会静默吞结果 → 一律写
   `command grep`；`go test ./...` 必须在 `go/` 子目录里跑（module 根在那）；
   `/bin/sh` 是 dash，没有 `exec -a`，测试里要「argv0 是特定名字」得用
   `/bin/bash -c "exec -a …"`。
3. **你自己的请求也穿行在这个 daemon 里**。root 的 claude 被 PATH shim 接管，
   `newgate stop` 和 `newgate start` **绝不能拆成两个工具调用**——中间那个请求
   会撞上死掉的代理，会话当场挂。一切重启都放**一条命令**里（见 §3）。

---

## 1. 开发

```bash
cd go
make build      # 本机二进制 → go/bin/newgate（dynamic，本机够用）
make static     # CGO_ENABLED=0 全静态，跨机器部署必须用这个
make test       # == go test ./...
go vet ./... && gofmt -l component modules cmd
```

分层（依赖通过 capability 表达，`component` 不 import 业务模块）：

| 目录 | 管什么 | 改这里当你在做… |
| --- | --- | --- |
| `component` | typed capability、依赖 DAG、生命周期（强依赖 `Need` / 弱依赖 `Optional`，单阶段装配），以及装配过程的 trace 出口 | 组件框架本身 |
| `modules/config/{domain,resolve,roleprov,store}` | Config 组件、配置语义、动态角色、fallback 纯函数、持久化（后三个同时是**共享叶子**，谁都能直接 import，不算依赖边） | 档位与配置 |
| `modules/gateway/{forward,special,rewrite,thinkcache}` | 网关组件及其内部实现 | 转发、扩展与思维链 |
| `modules/breaker` | binding 健康表：可用性 + 延迟排序。`Need(gateway)`——它是**插进数据面四个决策点的策略**，不是被 gateway import 的库（方向 2026-09-18 翻过） | 熔断策略与恢复 |
| `modules/confighook` | agent/config/state-field 注册端口 | 配置文件接管 |
| `modules/runtime/{launch,injection,takeover}` | 接管与 env 注入 | 客户端怎么被拦下来 |
| `modules/cli` | 命令行界面：分派、排版、注入点（**不含任何业务知识**，见 §4） | `newgate <动词>` 的分派与 help |
| `modules/<客户端>` | Claude Code、opencode 的接入与组合行为（**客户端**留 core） | 新增客户端支持 |
| **发行版的模块**（上游怪癖修补、客户端×交叉语义） | **不在本仓库**：住在发行版仓库里，那是**另一个 Go module** | 修某个上游的怪癖 |
| `app`（在 `modules/` 之外） | 组合根：装图、把这次调用交给入口。**不认识任何模块**；对内提供 `Selection`/`Main` 这条接缝 | 换装图方式 |
| `root`（唯一的 built-in，不在 `modules/` 下） | 入口账本：谁认领这次进程调用 | 加入口类型 |
| `tools/genmodules` | 构建期扫描 `modules/`，生成装配清单（**只看本仓库、完全离线**） | 改模块发现规则 |
| `tools/genmodules/scan` | 「哪些目录算组件」+「目录名怎么拧成 import 别名」的唯一实现（发行版的生成器也用这份） | 改组件判据 |
| `testing/{testkit,upstream,system}` | 测试设施：模块层起图、进程内假上游、系统层整图。**从这里起是对外 API**（发行版拿 `system.StartWith` 测自己的模块） | 加测试设施（不是产品代码） |

每个 `modules/<name>`（无例外）都必须在根目录提供唯一的 `module.go`，由它用
`New()` 直接返回 `component.Component`，并声明 `Requires`、`Provides`、`Start`
和 `Stop`。**生命周期只有这两个阶段**：没有 `Attach`，也没有「注入阶段」（见 §4）。
注册型 capability 必须返回 `component.Release`，consumer 在 `Stop` 中逆序释放。
复杂实现可以拆文件或子包，但入口文件名和所在层级不能变化。

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

### 发行版：另一个 Go module（2026-09-20 起）

**core 只是内核**：链的复合、结局的推进、身份的定义、故障的隔离。**哪些模块属于
这个产品**是发行版的事——发行版是**另一个仓库、另一个 Go module**，它把 core
当依赖（`replace` 到它自己的 `core/` submodule），并把自己那张表交给内核的组合根：

```go
app.Main(ctx, app.Options{Loader: app.Selection{
    Disable: []string{"cli"},                        // 关掉内核的界面（写**目录名**）
    Extra:   []app.Entry{{Dir: "deepseek", Component: deepseek.New()}},
}})
```

装模块/关模块**都不改 core 的代码**，core 也不认识任何模块名。

**2026-09-20 之前不是这样**：那时内核根挂着一张 `modules-ext.json`（Pin），构建期
把发行版仓库 clone 到 `go/modules-ext/` 再扫一遍。那套机制整个删了，因为产品决定
住进了内核里，代价实测三条：编一次发行版就把内核 checkout 改脏（生成的清单被重写成
发行版那份，挡住 pull/换分支）、内核 CI 必须去 clone 另一个仓库、装配清单 import 了
发行版的包（核心里出现一条指向发行版的编译期依赖）。现在内核**完全离线**：

```bash
cd go && make check          # 本仓库自己的 fmt/vet/清单/单测/两条 e2e
GOPROXY=off go test ./...    # 离线也全过——「不需要网络」是事实不是承诺
```

`app/independence_test.go` 守着这条边界（Pin 文件、`modules-ext` 的 import 路径、
`extmanifest`、`NEWGATE_MODULES_PIN` / `NEWGATE_EXT_DRYRUN`，一个都不许回来）。

- 官方发行版：`git@github.com:rzbdz/newgate-ext.git`，`main` = 官方发行版，
  `template` = 给别人 fork 的骨架。**fork 它 + `build/build.sh` 就是一个新发行版**。
- **改发行版模块请去发行版的工作目录**（如 `/root/workspace/newgate-ext`），那是个
  独立的 Go module，改完直接提交推送；要动内核就去内核仓库改、推，再回来挪 gitlink。
- 装不装发行版都要能编：`TestRemovalMatrix`（app/matrix_test.go）逐个模块摘一遍，
  **只有 built-in（root）不可摘**；`app/direction_test.go` 与
  `modules/gateway/direction_test.go` 守着「组合根/数据面不认识任何具体模块」。
- 测试边界：**内核的测试只管内核的逻辑与内核的模块**；发行版模块的行为（DeepSeek
  的尾部形状、推理回填、跨上游迁移）由发行版自己的测试与 `mock/` 端到端锁。

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
  - 模块：`go/testing/testkit` —— `testkit.Start(t, 我, 桩...)` 装出「我 + 我的
    依赖链」的真组件图，断言接口通过性和注册/撤销对称性；`testkit.Sandbox(t)`
    圈住 `NEWGATE_HOME`/`NEWGATE_TARGET_DIR`/`HOME`；
  - 系统：`go/testing/system` —— `system.Start(t)` 起**整张**真组件图 + 真转发
    服务（**临时端口，不是 8899**，所以能和跑着的线上 daemon 并存）+ 进程内假
    上游（`go/testing/upstream`，两方言 + 流式 + 严格 reasoning + 故障注入）。
    不是 e2e：没有子进程、没有 PATH shim、没有真接管；
  - 端到端：`mock/*.sh` —— 真二进制、真进程、真接管。**刻意与 Go 侧解耦**
    （不 import 任何 core 包），所以 Go 怎么重构都不该影响它。它一红就是行为
    真的变了。
- **一把梭**：`cd go && make check`（格式 + vet + 生成清单 + 单测 + 两条零 token
  端到端）。拆开：`make test` / `make test-race` / `make e2e` / `make e2e-claude`。
- **单测不出网**（`docs/10-testing-security.md` §1）：一律用 `httptest`，出网即失败。
- **端到端零 token**：`make e2e-claude`（假上游 + 沙箱 `NEWGATE_HOME`，不碰真实
  配置）。改网关/插件行为后跑一遍，它锁的正是真实现场复现出来的那几条。
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

## 3. 部署（优雅，零停机）

### 3.1 单机

```bash
cd go && make build                      # 跨机器用 make static
cp bin/newgate /root/.local/bin/.newgate.new
mv -f /root/.local/bin/.newgate.new /root/.local/bin/newgate   # 换名覆盖
/root/.local/bin/newgate restart         # 优先走优雅交接（socket fd 移交）
```

- **为什么不是 `cp` 直接覆盖**：Text file busy——运行中的进程占着 inode。
  `mv`（rename）换目录项，老进程继续用旧 inode 排空在途请求。
- `newgate restart` 先探 `daemon.Running()`（读 pidfile）。**pidfile 里的 pid
  是死的**时它会误判「没在跑」，退化成 stop+start（摘接管、起新进程、撞端口，
  2026-09-15 实际踩过）。
- 手动优雅交接（CLI 判定「没在跑」时唯一不停机的换版手段）：
  ```bash
  TOK=$(python3 -c 'import json;print(json.load(open("/root/.config/newgate/state.json"))["control_token"])')
  curl -s -X POST -H "Authorization: Bearer $TOK" http://127.0.0.1:8899/__newgate/upgrade
  ```
  返回 `{"ok":true,"new_pid":…}` 才算数；被拒时旧进程继续服务，服务不受影响。
- **换完必须核验**（别信 `newgate version`，那是 CLI 的版本）：
  ```bash
  PID=$(python3 -c 'import json;print(json.load(open("/root/.config/newgate/.newgate.pid"))["pid"])')
  md5sum /proc/$PID/exe /root/.local/bin/newgate     # 两个必须一样
  ```
- **重启会清空内存里的 thinkcache**（推理内容缓存）。磁盘冷层
  （`thinkcache.bin`）从 2026-09-16 起可用，重启后会自动装回去；冷层坏掉时
  重启后的第一发大请求会因为思维链补不上而被上游 400（`must be passed
  back`）。长会话中间重启前先想一下这件事。
- **多用户/权限坑**（daemon 以 `claude` 用户跑，配置目录 root 建的）：
  root 的 CLI 写出的文件被 umask 削成 `0640 root:developer` → claude 写不了。
  表现为：优雅交接 500（`AdoptRuntime` EACCES）、`thinkcache 落盘关闭（继续
  纯内存）: permission denied`、400 证据「已存」其实没写出。修法一行：
  `chmod 0660 <文件>` / `chmod 2770 <目录>`。

### 3.2 跨机器（`panjunzhong@10.0.50.11`，snode1）

```bash
cd go && CGO_ENABLED=0 make static        # 必须静态：那台 glibc 旧，动态链接起不来
scp bin/newgate panjunzhong@10.0.50.11:/data/home2/panjunzhong/WorkSpace/bin/newgate.new
ssh panjunzhong@10.0.50.11 'cd /data/home2/panjunzhong/WorkSpace/bin && \
  mv -f newgate.new newgate && ./newgate restart'
```

同样用 `/proc/<pid>/exe` 的 md5 核验。远程 daemon 以 `panjunzhong` 跑，pidfile
同用户，交接不会撞权限坑。`/home/panjunzhong/WorkSpace` 是
`/data/home2/panjunzhong/WorkSpace` 的软链，同一份。

### 3.3 接管配置的更新

改了注入内容（档位名、base URL、槽位映射）后，**已经跑着的会话不会自动变**
（env 是启动时注入的），要重新接管一次：

```bash
newgate on claude      # 重装 PATH shim（claude 下次启动生效）
newgate on opencode    # 重写 opencode.json / oh-my-openagent.json
```

注意 `on` 对「已经是 `newgate/xxx` 的值」是幂等的——**不会重新分类**。要让
omo 的 intra-agent 槽位按新规则重分类，得走一轮
`newgate off opencode && newgate on opencode`（会先还原原始配置再改写）。

**omo 槽位（2026-09-16 起）**：`newgate on opencode` 给每个 intra-agent 分配
一个稳定键（`newgate/omo-sisyphus` / `newgate/cat-deep`），体格与建议写进
`~/.config/newgate/omo-slots.json`（见 `docs/04-configuration.md`）。重跑 `on` 是安全的：`current`
沿用它现在的档位（**不改行为**），原模型名从 `backups/original/` 找回。
**顺序不能反**：先换二进制、重启 daemon，再 `on opencode`——老 daemon 不认识
槽位键，配置先改了会让 opencode 的请求打空。看现状/调归属：`newgate omo`。

---

## 4. 代码约定

- **纯字节手术**：请求体不做 JSON 往返（大整数会变 `…92`，字段顺序和未知键会
  丢）。用 `gateway/rewrite` 的原语，只动该动的那一段。
- **不静默**：改写了请求就必须回报 `notes`，由调用方写日志。用户永远能知道
  自己的请求被动过哪一笔。
- **fail-open**：插件/探针出错就当它没跑过，请求按原样发出去。宁可上游报错，
  也不能因为一个补丁把整条链弄断。
- **注释写「为什么」**：现场报错原文、实测数字、日期、以及「为什么不是另一
  种做法」。这是这个仓库最重要的资产。
- **文档同步**：动了行为就改 `docs/`（`00-index.md` 有全表）。新机制写进
  `docs/09-extension-guide.md`，配置字段写进 `docs/04-configuration.md` / `docs/05-gateway.md`。
- **模块的键不 hard-code 进 core**：客户端插件自带的东西（omo 的
  sisyphus/librarian 槽位）由模块实现 `config.RoleProvider` 注册成动态角色
  键（`domain.ExtraRole`），命名与缺省归属留在 `modules/opencodeomo`。
  core 只认「键 → 缺省绑定」这张表，解析路径与档位完全一样（引用展开，见
  `resolve.BuildChain`）。接一个新插件 = 新注册一个 Provider，core 不动。
- **ui 只是一类普通模块，cli 是其中一个**（2026-09-18 定）。将来 tui / web 都会
  是独立模块，走同一套模式。由此推出三条硬规矩：

  - **业务模块不依赖任何 ui**。它们往当前存在的 ui 注入自己的控制面，谁在就注入
    给谁，谁不在就跳过。
  - **没装任何 ui 时，一切功能照常**。最坏情况只是「这些模块没有入口」——
    不影响任何模块的功能与运作；反过来，要操作某个模块的控制面，**至少得装
    一个 ui 模块**。
  - **ui 自己不依赖任何模块**。`modules/cli` 的 `Requires` 是**空的**，目录里不
    含任何业务知识。判断一个东西该不该留在 ui：**把它删掉，业务模块还成立吗？**
    成立就不该留在 ui。

  **注入是一条弱依赖**（`component.Optional(cli)`）。往界面里注册命令/状态行的
  模块声明它：**界面在，就注册进去；界面不在，就跳过**——模块功能一个都不少，
  只是没有入口。

  **为什么现在能用 `Optional` 了**：因为它是一条**排序边**，而排序边会不会成环
  取决于界面有没有出边——界面现在**一条出边都没有**（`cli.Requires` 是空的），
  箭头只有「模块 → 界面」一个方向。2026-09-18 之前界面依赖那些模块才能渲染，
  两条箭头互指成环，于是被迫引入 `component.Inject`（不排序的注入边）+
  `Component.Attach`（全图 `Start` 之后再跑一遍的第二阶段）来绕开。出边砍干净
  那套机制就整个删掉了——它带来的代价是实测出来的：顺序**没有任何保证**（当时
  9 个注入者恰好都在 cli 之后，靠目录名字母序碰巧成立）、停止顺序跟着失去保证
  （`breaker` 在 `cli` 之后停）。现在顺序由依赖图给出，逆序停止时注入者的
  `Stop` 一定先跑完，界面最后停。

  `app/` 里三条棘轮测试守着这个方向：
  `TestCLIDependenciesOnlyShrink`（界面出边集合必须为空）、
  `TestUIStaysOutOfTheDependencyGraph`（对 ui 只许 `Optional`，不许 `Need`）、
  `TestInjectorsStartAfterTheUI`（注入者必须排在界面之后）。

  **界面全部的能力都来自注入**，端口都在 `modules/cli/extension`：

  | 端口 | 交什么 | 谁交（举例） |
  | --- | --- | --- |
  | `RegisterCommand` | 一条命令（含 `HelpLine` 声明的槽位与位置） | 每个拥有动词的模块 |
  | `RegisterStatus` | status 里的一行 | gateway（代理）、runtime（接管）、config（配置） |
  | `RegisterStatusBlocks` | status 里的成块内容（表格） | config（档位绑定、fallback 链） |
  | `RegisterDiagnostics` | doctor 里的一项 | config、gateway、runtime |
  | `RegisterDump` | 诊断包（`alllogs`）里的原始素材 | config、gateway、runtime |
  | `RegisterGlossary` | 帮助屏术语表里属于自己的一行 | config-hook（agent）、config（槽位键） |
  | `RegisterVerbose` | 「我的详细模式开着」 | gateway（debug） |

  于是 `--help`、`status`、`doctor`、`alllogs` 全是**纯汇总**：里面每一行、每张表、
  每段原文都由拥有那份数据的人产出，**装一个新模块不需要改这四条命令里的任何一行**。

  两条可选接口让命令自己声明例外，省得界面列名单：`Handoff`（这条命令马上要把
  控制权交给别的进程）、`Unstyled`（这一次的输出不是版式——JSON / KV 原文 / 日志）。
- **两条棘轮测试**守着上面那些「方向」：`app/direction_test.go`（组合根不 import
  任何 `modules/…`）、`app/matrix_test.go`（摘除矩阵 + 零依赖模块独立加载）、
  `modules/gateway/direction_test.go`（数据面不认识任何策略）。加模块/加依赖前先
  想一下它们会不会红——红了就是方向被掰回去了。
- **提交**：英文 subject（`fix(scope): …` / `feat(scope): …`），正文说明
  「现场是什么样、为什么这么改、验证了什么」。概念的设计动机写在所属
  package 或声明旁；行为变化才更新专题文档。结尾只带
  `Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>`。
- **新的上游怪癖** → 写成 `gateway/special/st-<上游>.go` 插件，别散落在转发热
  路径的 `if` 里；插件自己说清 `Match`（认哪个上游）、`Why`（为什么存在）、
  `Apply`（改了什么）。用户能 `newgate st off <名字>` 单独摘掉。
  只对某一类客户端生效的补丁，判据用 `special.claudeCode(r)`，别按
  「Agent 非空」猜。
- **新的数据面策略**（摘谁的牌、记哪本账、什么算失败、留什么痕）→ 实现
  `gateway/policy` 的口，在自己的 `Start` 里 `RegisterFilter` 挂进去；**别让
  gateway import 你**——数据面的非测试源码里连你的名字都不该出现（`modules/
  gateway/direction_test.go` 那条棘轮测试守着方向：它拒「名字」也拒 `import`
  策略包本身，只有 `…/breaker/status` 那种 wire 叶子除外）。一个都不装时网关是
  最小系统，照常转发。见 `docs/09-extension-guide.md` §7。

---

## 5. 排查手册

```bash
newgate status            # 端口/请求数/失败数/thinkcache 计数
newgate logs              # 代理日志尾部（跟随后续用 tail -f ~/.config/newgate/newgate.log）
newgate doctor            # 环境体检：provider、链、接管、端口冲突
newgate tier <档位>       # 某个档位现在解析成哪条链、谁被跳过、为什么
newgate probe [profile]   # 主动探活：连通性 + 方言 + 上游毛病
newgate metrics           # 计数器：超时/转移/改写/取消，调参看这个
newgate st                # 当前生效的 special 插件与它们为什么存在
```

- 报文级证据：`~/.config/newgate/dump/`（`NEWGATE_DUMP=1` 时逐条存 in/out；
  上游 4xx 时无条件存 `err-<code>-req<id>.{client-sent,we-sent,upstream-said}`）。
  排查「是不是代理改坏了请求」这是唯一能拿出手的东西。
- 日志里 `X-Newgate-Chain` / `X-Newgate-Route` 响应头告诉你这一发实际走了谁。
- 上游报错原文始终原样转给客户端并在日志里打全（`上游原文:` 那行）。
