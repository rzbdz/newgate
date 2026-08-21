// Package policy 是准入策略层：决定一个候选**当下**能不能用。
//
// 与 resolve 的分工：resolve 负责「候选序是什么」（偏好，静态），
// policy 负责「这个候选现在准不准」（禁用 / 太慢 / 太贵）。
// 两者都是纯函数，把外部事实（时钟、探测数据）作为入参传进来。
//
// 设计见 docs/18-role-preference.md §7-§8。
package policy

import "time"

// TODO(M2): 带 TTL 的禁用。
// 关键约束（docs/18）：
//   - 必须带过期时间。永久禁用会变成「三个月前禁的早忘了」，
//     和「切了没生效」是同一类故障
//   - 匹配形式：provider/model、provider/*、*/model、role:pattern
//   - 必须记 reason，否则事后无法解释
type Disable struct {
	Pattern string
	Until   time.Time // 零值 = 永久（需 --forever 显式指定）
	Reason  string
	At      time.Time
}

// TODO(M2): Matches 判断某个 (role, provider/model) 是否被这条规则禁用。
func (d Disable) Matches(role, target string, now time.Time) bool {
	panic("TODO(M2): 见 docs/18 §7")
}

// TODO(M2): Mood 是容忍度倍率。
// 设计要点：延迟预算放在**档位**上（任务性质决定），倍率放在 mood 上
// （当下心情决定）。一个旋钮同时调所有档位，各自保持相对宽严。
type Mood struct {
	Name              string
	LatencyMultiplier float64
}

// TODO(M2): Budget 某个档位在当前 mood 下的延迟预算。
// 只用**首字节**延迟判断，绝不用总时长——探测是 4 token、真实请求可能
// 97KB，只有首字节可比。这条很容易被后来者改错。
func Budget(roleBudgetMs int, m Mood) time.Duration {
	panic("TODO(M2): 见 docs/18 §7")
}

// TODO(M2): 迟滞，防止延迟在预算线附近摆动导致会话内模型来回跳
// （每跳一次 prompt 缓存就作废）。超预算 120% 才降级，回到 80% 才恢复。
const (
	DemoteRatio  = 1.2
	PromoteRatio = 0.8
)

// TODO(M2): 全部候选被过滤光时的策略。
// 默认 relax：逐条放宽（先忽略成本 → 再忽略延迟 → 最后只看活着），
// 但每次放宽都必须告警——绝不静默。pinned 的 profile 不适用 relax。
type Exhausted string

const (
	Relax  Exhausted = "relax"
	Strict Exhausted = "strict"
)
