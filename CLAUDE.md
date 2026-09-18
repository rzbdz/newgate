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
| `component` | typed capability、依赖 DAG、生命周期 | 组件框架本身 |
| `modules/config/{domain,resolve,roleprov,store}` | Config 组件、配置语义、动态角色、fallback 纯函数、持久化 | 档位与配置 |
| `modules/gateway/{forward,special,rewrite,thinkcache}` | 网关组件及其内部实现 | 转发、扩展与思维链 |
| `modules/breaker` | binding 健康表：可用性 + 延迟排序，**无 Requires**（叶子，被注入数据面与 CLI） | 熔断策略与恢复 |
| `modules/confighook` | agent/config/state-field 注册端口 | 配置文件接管 |
| `modules/runtime/{launch,injection,takeover}` | 接管与 env 注入 | 客户端怎么被拦下来 |
| `modules/cli` | CLI 组件与命令壳 | `newgate <动词>` |
| `modules/<客户端或模型>` | Claude Code、DeepSeek、组合行为 | 新增可组合组件 |
| `app`（在 `modules/` 之外） | 组合根：装配清单与 `App` 所有权对象 | 换默认装配 |
| `tools/genmodules` | 构建期扫描 `modules/` 生成装配清单 | 改模块发现规则 |
| `testing/{testkit,upstream,system}` | 测试设施：模块层起图、进程内假上游、系统层整图 | 加测试设施（不是产品代码） |

每个 `modules/<name>`（无例外）都必须在根目录提供唯一的 `module.go`，由它用
`New()` 直接返回 `component.Component`，并声明 `Requires`、`Provides`、`Start`
和 `Stop`。注册型 capability 必须返回 `component.Release`，consumer 在 `Stop`
中逆序释放。复杂实现可以拆文件或子包，但入口文件名和所在层级不能变化。

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
- **提交**：英文 subject（`fix(scope): …` / `feat(scope): …`），正文说明
  「现场是什么样、为什么这么改、验证了什么」。概念的设计动机写在所属
  package 或声明旁；行为变化才更新专题文档。结尾只带
  `Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>`。
- **新的上游怪癖** → 写成 `gateway/special/st-<上游>.go` 插件，别散落在转发热
  路径的 `if` 里；插件自己说清 `Match`（认哪个上游）、`Why`（为什么存在）、
  `Apply`（改了什么）。用户能 `newgate st off <名字>` 单独摘掉。
  只对某一类客户端生效的补丁，判据用 `special.claudeCode(r)`，别按
  「Agent 非空」猜。

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
