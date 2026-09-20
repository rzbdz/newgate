<div align="center">

# newgate

### 一个会挑模型的网关，它的**机制**——里面没有任何产品。

<sub>档位路由与有序回退 · 认得出是谁的错的熔断 · 重启不掉请求 · 一切皆模块 · 零依赖</sub>

[![CI](https://github.com/rzbdz/newgate/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)
![build](https://img.shields.io/badge/build-fully_offline-blue)

<sub><a href="README.md">English</a> · <b>中文</b></sub>

</div>

---

内核是「适配很便宜」的原因：档位、回退、熔断、升级路径都是**机制**，所以来了新模型、
或者某个上游冒出新怪癖，那是别人在自己的发行版里加一个模块——不是改内核。

**你的 CLI 只说档位，网关决定用哪个模型。**
`heavy` 不是一个上游，是一条有序的候选链——先按你的规则排，再按预测的首字节时间排。
应答会告诉你它最后走了哪里。

**单个上游挂掉，不等于你挂掉。**
每一次跳过都有解释，每一次改道都有日志。某个 provider 不可用、被限流、或者答得不对，
那是一次路由决策，不是一次故障。

**认得出是谁的错的熔断。**
可用性、限流、配置三类失败各有各的账本、各有各的阈值。因为**你的请求形状不对**而吃到的
400，谁的账都不记。半开试探自己会恢复。

**重启不掉请求。**
监听 socket 交给新进程，旧进程把在途请求排空——流式响应也一样。会话中间升级是安全的。

**一切皆模块——包括你以为会很特殊的那几块。**
网关、熔断、界面、入口、消息目录。耦合靠 capability，没有谁 import 同伴；「摘不掉」是
从组合根消费的端口推出来的，永远不是从一张名单。

**零依赖，完全离线可编。**
没有第三方包，没有 `go.sum`；测试在 `GOPROXY=off` 下全过。CJK 宽度、消息目录、终端
版面都是手写的。

## 这里**没有**什么

没有上游怪癖、没有客户端接管、没有任何人替你选的档位。装哪些模块、按什么顺序装，是
发行版的决策——它自己的仓库、自己的模块清单、自己的策略，谁想要就 fork 去改：

```go
app.Main(ctx, app.Options{Loader: app.Selection{
    Disable: []string{"cli"},                                        // 按目录名
    Extra:   []app.Entry{{Dir: "deepseek", Component: deepseek.New()}},
}})
```

内核里没有一处代码会因为产品名而分叉——`deepseek` 只出现在注释、测试和开发工具里。
从 [**rzbdz/newgate-ext**](https://github.com/rzbdz/newgate-ext) 拿一个现成的，或者
自己写一份选择，组装你真正想要的那个网关。

## 快速开始

```bash
make build            # → bin/newgate —— 只有内核模块：一台机制试验台
bin/newgate init && bin/newgate start
bin/newgate doctor    # 出问题时
```

## 文档

[docs/00-index.md](docs/00-index.md) —— 地图 ·
[docs/03-architecture.md](docs/03-architecture.md) —— 归属，以及和发行版之间的那条界线 ·
[CLAUDE.md](CLAUDE.md) —— 贡献者（人或 agent）要守的家规。

## 许可

Pre-1.0，还在动。想依赖它做东西之前先问一声。
