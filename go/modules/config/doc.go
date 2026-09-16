// Package core 是无 IO 的领域层。
//
// 规则（docs/02-architecture.md §2）：**core 不 import 任何 IO**。
// 没有文件系统、没有网络、没有 os/exec。这样「给定配置快照 + 输入，
// 应该解析出什么、生成什么计划」全部可以纯函数测试。
//
// 子包：
//
//	domain    实体与值类型：Provider / Binding / Profile / Role / Tool / Session
//	resolve   解析流水线：fallback 链构造（纯函数）
//	injection Injection 接口与 Plan 类型（plan() 是纯的，apply() 在 runtime）
//	protocol  协议方言、模型名规范化
//	policy    准入策略：禁用、mood、延迟预算（M2）
//
// 依赖方向严格单向，core 是最内层，谁都可以依赖它，它不依赖任何人。
package core
