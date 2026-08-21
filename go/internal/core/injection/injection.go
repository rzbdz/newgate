// Package injection 定义「把解析结果落地到工具进程」的抽象。
//
// plan() 是**纯函数**——这样 --dry-run 的输出就是真实将要发生的事，
// 而不是另写一套「大概会这样」的描述。apply/rollback 有副作用，
// 实现在 runtime/injection。
//
// 设计见 docs/04-injection.md（注意：该文档写于 v1「每次运行都注入」
// 时期，v3 下注入降级为一次性 bootstrap，顶部有说明）。
package injection

// Kind 注入方式。
type Kind string

const (
	// KindShim PATH shim：把工具名符号链接到 newgate，在子进程里注入 env。
	// 这是唯一能盖过用户 shell rc 里已 export 的变量的方式——写配置文件
	// 会被静默盖掉，是最难查的那类故障。claude 走这条。
	KindShim Kind = "shim"
	// KindConfigFile 合并式改写工具的配置文件，只注入我们的键。
	// opencode / oh-my-openagent 走这条。
	KindConfigFile Kind = "config"
	// TODO(M6): KindEnvProxy 透明拦截——注入 HTTPS_PROXY + 作用域受限的 CA。
	// CA 绝不装进系统信任库，只有 newgate spawn 的子进程通过 env 信任它
	// （docs/11 §3.2）。
	KindEnvProxy Kind = "env-proxy"
	// TODO(M6): KindPreload Node 系进程内劫持（NODE_OPTIONS --require），
	// 不需要 CA、不需要 MITM（docs/11 §4）。
	KindPreload Kind = "preload"
)

// Plan 「要做什么」。纯数据，可打印、可 diff、可 review。
type Plan struct {
	Kind Kind
	Tool string
	// Env 要注入子进程的环境变量。
	Env map[string]string
	// Unset 必须从子进程环境删掉的干扰变量。这是「切了没生效」最常见的
	// 成因：用户 shell 里残留的老变量盖过我们注入的。
	Unset []string
	// FileOps 要做的文件操作（config 注入才有）。
	FileOps []FileOp
	// Notes 人类可读的说明，--dry-run 打印这个。
	Notes []string
}

// FileOp 一次文件改写。必须记录**精确的**键级操作和每个键的原值
// （包括「原来不存在」这个状态），否则无法做反向 patch。
type FileOp struct {
	Path   string
	Format string // json | jsonc | toml | yaml
	// Patches 键路径 → 新值。
	Patches []Patch
}

type Patch struct {
	Pointer string // JSON Pointer
	Value   interface{}
	// TODO: OldValue + Existed，用于反向 patch（docs/04 §3.2 冲突处理）
}

// Applied 回滚句柄。
type Applied struct {
	Plan       Plan
	BackupPath string
	// TODO(M2): JournalPath —— 崩溃恢复。只有 inplace 改写才需要，
	// 一次性 bootstrap 不需要（docs/16 §3）。
}

// Injector 一种注入方式的实现。
type Injector interface {
	Kind() Kind
	// Probe 当前环境下这种注入可用吗？不可用要给出原因，供降级和 doctor 用。
	Probe(tool string) error
	// Plan 纯函数：算出要做什么，不产生任何副作用。
	Plan(tool string, port int) (Plan, error)
	// Apply 执行副作用。
	Apply(p Plan) (Applied, error)
	// Rollback 幂等：重复调用、部分失败后调用都必须安全。
	Rollback(a Applied) error
}
