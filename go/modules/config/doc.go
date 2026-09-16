// Package config owns newgate's configuration capability and its domain,
// resolution, persistence, and path implementation.
//
// The domain, resolve, injection, and policy subpackages remain pure. Filesystem
// IO is isolated in store and paths behind the component-owned subsystem.
//
// 子包：
//
//	domain    实体与值类型：Provider / Binding / Profile / Role / Tool / Session
//	resolve   解析流水线：fallback 链构造（纯函数）
//	injection Injection 接口与 Plan 类型（plan() 是纯的，apply() 在 runtime）
//	policy    准入策略：禁用、mood、延迟预算（M2）
//	store     配置快照、原子读写和热更新
//	paths     配置文件位置
package config
