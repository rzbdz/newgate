# 配置与路由解析

## 1. 磁盘布局

默认配置根目录是 `~/.config/newgate`，可用 `NEWGATE_HOME` 在测试或独立实例中
覆盖。主要内容包括：

```text
providers.json       provider、状态和 daemon 配置
mappings/*.kv        profile 的 role 绑定
state.json           当前 profile、接管状态和控制 token
newgate.log          daemon 日志
thinkcache.bin       reasoning cache 冷层
dump/                请求级取证
```

写入使用同目录临时文件和 rename，避免读到半份 JSON。Watcher 根据文件变化重新
加载完整快照；失败时保留上一份有效快照并报告错误。

## 2. Provider

Provider 描述上游 base URL、认证、声明协议和可选双方言入口。真实密钥只用于
构造上游请求，不应写入日志、metrics 或错误正文。

Provider 名和 model 名组成 Binding：

```text
provider/model
```

## 3. Profile

Profile 为 role 提供候选 Binding 链。Resolver 是纯函数：输入 snapshot、Agent、
role 和请求覆盖，输出有序步骤以及被跳过候选的原因。

优先级由显式请求 profile、Agent profile 和全局默认共同决定。稀疏 profile
只覆盖写出的 role，其余继续向基础层解析。

## 4. Role 规则

```text
heavy > normal > mid > light
```

- `normal` 是主力；
- profile 没写 `normal` 时，`mid` 可承接；
- `vision` 独立判断，不参与强弱 fallback；
- 未知 role 不猜测，直接返回可诊断错误。

## 5. 动态 role

客户端扩展可以通过 Config capability 注册额外 role，例如 OMO 的 intra-agent
槽位。Config component 拥有 registry；Store 每次加载快照时刷新动态定义。
注册返回 Release，组件停止后不会残留。
