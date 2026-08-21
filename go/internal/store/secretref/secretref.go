// Package secretref 解析密钥引用。
//
// 铁律（docs/09-security.md §2）：配置文件里**永远存引用，不存明文**。
// 明文只在内存中存在，且包在 Secret 里；取明文只有 Reveal() 一个口子，
// 调用点可枚举、可审计。
package secretref

// Secret 包装明文密钥，防止误打印。
//
// TODO(M2): 目前 provider 直接存 api_key / api_key_env 字符串。
// 迁到这里之后所有日志、--dry-run、API 响应默认走 String()。
type Secret struct{ v string }

func (s Secret) String() string { return "***" }
func (s Secret) IsZero() bool   { return s.v == "" }

// Reveal 唯一取明文的口子。加 lint 规则限制调用位置：
// 只允许在「构造子进程 env」和「网关发上游请求」两处出现。
func (s Reveal) Reveal() string { panic("TODO(M2)") }

type Reveal = Secret

// TODO(M2): Resolver 按 scheme 前缀解析。
//
//	env:<VAR>        从环境变量读
//	file:<path>      从文件读，要求权限 <= 0600
//	keychain:<s>/<a> OS 钥匙串
//	cmd:<argv-json>  执行命令取 stdout（有执行风险：只信任用户自己
//	                 config 目录下的该字段；import 外部配置时默认剥离）
//	plain:<value>    明文，写入时警告
type Resolver interface {
	Scheme() string
	Resolve(ref string) (Secret, error)
}
