/*
Package component 是 newgate 的依赖与生命周期内核。

# 为什么需要它

newgate 的功能不是一棵固定调用树。客户端、模型家族、CLI 命令和请求修补都
可以独立加入；其中一些行为只在两个模块同时存在时成立。如果模块直接 import
彼此的具体实现，最终会出现循环依赖、中心注册表和无法撤销的 package init。

这里采用的最小模型是：模块声明“我需要什么能力、提供什么能力”，组合根只列出
组件集合，Manager 从声明中计算启动顺序。框架不认识 Config、Gateway 或
Claude Code；它只认识下面六个结构概念。

# 六个概念

内核用六个概念构造一张有向图：

  - Capability[T] 是服务或扩展点的有类型名字。NewCapability[T] 声明它；
    一个端口可以有零个、一个或多个提供者——基数不是框架的约束，谁需要
    "只能有一个"就在自己的 Get 调用点或业务逻辑里断言。
  - Requirement 表示组件消费一个 capability。Need 是**强依赖**：提供者必须存在，
    否则装配失败。Optional 是**弱依赖**：你存在，我就依赖你（连顺序一起）；
    你不在，我就不依赖你（不建边、不报错，只是拿不到端口）。两者都是排序边，
    所以「往别人那里注册东西的人」一定排在「被注册的那一方」后面。
  - Provision 把 capability 与它提供的具体值绑定。
  - Component 是一个图节点：它可以消费和提供 capability，在提供者之后启动，
    并在提供者之前停止。
  - Context 是传给 Start 的已解析端口表。Get 读取单值端口，
    GetAll 读取多值端口的所有贡献。
  - Manager 验证图、按依赖顺序启动节点，再按相反顺序停止节点。

关键点是“组件不是服务”。Component 只是生命周期与依赖声明；真正跨模块调用
的是 Capability[T] 中的 T。下面的 consumer 会在 provider 之后启动，却不需要
知道究竟哪个组件提供了 Store：

	var StoreCapability = component.NewCapability[Store]("config.store")

	provider := component.Component{
		Name: "config",
		Provides: []component.Provision{
			component.Provide(StoreCapability, store),
		},
	}

	consumer := component.Component{
		Name: "cache",
		Requires: []component.Requirement{
			component.Need(StoreCapability),
		},
		Start: func(ctx context.Context, graph component.Context) error {
			store := component.MustGet(graph, StoreCapability)
			return startCache(ctx, store)
		},
	}

# 生命周期

Manager 的构建分为五步：

 1. 从 Loader 收集全部 Component。
 2. 验证名称、capability 类型和必需提供者。
 3. 拓扑排序，使提供者位于消费者之前。
 4. 按图顺序调用 Start。
 5. 启动失败或收到 Stop 时，按相反顺序调用 Stop。

**只有 Start 与 Stop 两个阶段**，没有「注入阶段」。端口绑定（Provides）在**任何
Start 之前**就已全部完成，所以受启动顺序影响的只有「谁在谁的 Start 里注册了什么」
——而那由依赖边保证：注册者排在 owner 后面。停止是启动的严格逆序，于是注册者的
Stop 一定先于被注册方，撤注册时目标还活着。

（2026-09-18 之前有过一个 `Attach` 第二阶段，给「不参与排序的注入边」用；随那条
边一起删掉了。为什么删、代价是什么，写在 Optional 的注释里。）

Start 之前只允许声明端口和值，不执行 IO。组件在 Start 中取得依赖并注册扩展。
注册 API 返回 Release；消费者保存这些句柄，并在 Stop 中调用 ReleaseAll。
这样卸载只会删除该组件拥有的贡献，不会误删后来注册的新值。

# 看得见的过程

装配过程（扫到哪些组件、各自声明了什么、哪条弱依赖没命中、排出来什么顺序、每个
组件起了多久）经 SetTrace 报出去。内核不认识日志，只提供出口；装不装、装什么由
组合根决定（`app.TraceToLogFile` 把每个进程的那一段追加进 newgate 日志，优雅交接
那次升级因此能事后逐行对照新旧两版）。

# 边界

内核刻意不知道配置文件、HTTP、模型、客户端或命令。这些概念属于各自模块；
对内核而言，它们只是普通的有类型 capability value。
*/
package component
