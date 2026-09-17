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
  - Requirement 表示组件消费一个 capability。Need 要求提供者必须存在，
    Optional 允许图中没有这个端口。
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
		Name: "gateway",
		Requires: []component.Requirement{
			component.Need(StoreCapability),
		},
		Start: func(ctx context.Context, graph component.Context) error {
			store := component.MustGet(graph, StoreCapability)
			return startGateway(ctx, store)
		},
	}

# 生命周期

Manager 的构建分为五步：

 1. 从 Loader 收集全部 Component。
 2. 验证名称、capability 类型和必需提供者。
 3. 拓扑排序，使提供者位于消费者之前。
 4. 按图顺序调用 Start。
 5. 启动失败或收到 Stop 时，按相反顺序调用 Stop。

Start 之前只允许声明端口和值，不执行 IO。组件在 Start 中取得依赖并注册扩展。
注册 API 返回 Release；消费者保存这些句柄，并在 Stop 中调用 ReleaseAll。
这样卸载只会删除该组件拥有的贡献，不会误删后来注册的新值。

# 边界

内核刻意不知道配置文件、HTTP、模型、客户端或命令。这些概念属于各自模块；
对内核而言，它们只是普通的有类型 capability value。
*/
package component
