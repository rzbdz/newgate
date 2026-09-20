package confighook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ConfigHooks 是客户端模块写入配置扩展的所有权端口。
// 每次注册都返回 Release，使模块停止时能精确撤销自己的贡献。
type ConfigHooks interface {
	RegisterAgent(*Agent) (modules.Release, error)
	BindTakeover(agentID string, takeover ConfigTakeover) (modules.Release, error)
	RegisterStateField(owner, name string) (modules.Release, error)
}

// AgentCatalog 是运行时和 CLI 的只读客户端目录，
// 与 ConfigHooks 分离后，消费者无法借查询能力修改注册表。
type AgentCatalog interface {
	Get(id string) (*Agent, bool)
	Names() []string
}

// 两个 capability 是同一注册表的读写分面：写端只交给扩展模块，
// 读端交给 runtime 和 CLI，防止查询者获得注册权限。
var (
	ConfigHooksCapability  = modules.NewCapability[ConfigHooks]("config-hooks")
	AgentCatalogCapability = modules.NewCapability[AgentCatalog]("agent-catalog")
)

// Slot 描述客户端的一个模型槽位如何映射到 newgate 语义档位。
// EnvVar 为空的槽位仍可供配置接管使用，但不会参与进程环境注入。
type Slot struct {
	Name string
	// Tier 是这个槽位的**缺省**归属。用户可以在配置里改（见 Agent.SlotTier）
	// ——改了之后生效的是那个，不是这个。所以读「此刻走哪儿」一律用 Agent.TierOf，
	// 别直接读它。
	Tier string
	// Also 是这个槽位**除档位之外**还认的取值——客户端自己的约定，不是我们的档位。
	//
	// 为什么要有它：Claude Code 的 subagent 槽位可以写 `inherit`（「跟父会话走」），
	// 那是**它的**语义，newgate 只是原样注入这个字符串。没有这一格的话，槽位映射
	// 那条可配置的路会把 `inherit` 当成打错的档位名滤掉——界面上设不了，手改的还会
	// 被静默忽略（看着像没生效，而说明里明明写着可以这么写）。
	//
	// 取值归**客户端模块**声明（那是它的知识）；内核与配置层只负责「这些也是合法的」。
	Also   []string
	EnvVar string
	Desc   string
}

// Agent 是一个可接管 AI CLI 的完整描述符。
// 它集中声明二进制发现、协议、模型槽位和配置接管，避免这些知识散落在 runtime。
type Agent struct {
	ID      string
	Bin     []string
	Dialect string
	Slots   []Slot

	BaseURLEnv string
	AuthEnv    string
	UnsetEnv   []string
	Notes      string
	Config     ConfigTakeover

	// ContextWindowEnv / AutoCompactEnv 是「窗口声明」两个环境变量的**名字**：
	// 前者走客户端对「未知模型」的窗口假设，后者是它自己的 compact 触发点。
	//
	// 名字是**客户端的知识**（Claude Code 叫 CLAUDE_CODE_MAX_CONTEXT_TOKENS），
	// 所以由 agent 定义填，内核只负责「配置里声明了就注进去」。
	// 空 = 这个客户端不认识这两种变量（比如 opencode），那就不注入。
	//
	// 2026-09-20 之前这两个名字硬编码在 modules/runtime/launch 里——那是内核在
	// 认识某一家客户端的内部变量名，方向反了。搬到定义上是同一个动作的延续：
	// 客户端接入归发行版（见发行版的 modules/claudecode），内核只提供这张表。
	ContextWindowEnv string
	AutoCompactEnv   string

	// SlotTier 说「这个槽位**此刻**走哪个档位」，覆盖 Slot.Tier 那个缺省；
	// 返回空串 = 没有覆盖，用缺省。nil = 这个客户端不支持改。
	//
	// # 为什么是一个函数，而不是让内核去读某个配置键
	//
	// 「槽位 → 档位」是**客户端自己的产品决定**，而那句话现在可以改。改它的地方
	// 是客户端模块自己的配置（claudecode 的映射住在 claudecode 的键里），内核不认识
	// 任何一家的键名——那正是「客户端接入归发行版」这条边界。内核只问一句「现在呢」，
	// 怎么算出来是模块的事。
	//
	// 为什么不是每次 Start 时把 Slot.Tier 改掉重新登记：登记只发生一次，而用户改
	// 映射发生在之后（界面上点一下）。回调每次注入时现问，改完**下一次接管就生效**，
	// 不必重启 daemon、更不必重新登记。
	SlotTier func(Slot) string
}

// TierOf 返回槽位此刻实际走的档位：有人覆盖就问它，否则用定义里的缺省。
//
// **所有读「这个槽位走哪儿」的地方都必须走这里**（注入、钉死模式下解析真实模型名、
// 以及 `newgate config` 那张表）——直接读 Slot.Tier 的地方会安静地显示/注入缺省值，
// 而用户明明改过（改完界面显示变了、行为没变，是最难查的一种不一致）。
func (a *Agent) TierOf(slot Slot) string {
	if a.SlotTier != nil {
		if t := a.SlotTier(slot); t != "" {
			return t
		}
	}
	return slot.Tier
}

// TakeoverReport 记录一次配置接管实际改了什么；接管不能静默成功。
type TakeoverReport struct {
	File     string
	Rewrites []string
	Fuzzy    int
	Skipped  string
}

// ConfigTakeover 定义可逆的持久配置接管。
// Apply 与 Restore 成对，Targets/IsTakenOver 则让 CLI 能在执行前解释当前状态。
type ConfigTakeover interface {
	Targets() []string
	IsTakenOver(target string) bool
	Apply(port int) ([]*TakeoverReport, error)
	Restore() ([]string, error)
}

// EnvSlots 返回需要注入环境变量的槽位，同时保留原始 Slots 供配置型客户端使用。
func (a *Agent) EnvSlots() []Slot {
	var out []Slot
	for _, slot := range a.Slots {
		if slot.EnvVar != "" {
			out = append(out, slot)
		}
	}
	return out
}

// BuildEnv 生成启动客户端所需的最小环境覆盖，不复制当前进程的完整环境。
func (a *Agent) BuildEnv(port int, authToken string) map[string]string {
	env := map[string]string{}
	if a.BaseURLEnv != "" {
		env[a.BaseURLEnv] = fmt.Sprintf("http://127.0.0.1:%d/a/%s", port, a.ID)
	}
	if a.AuthEnv != "" {
		env[a.AuthEnv] = authToken
	}
	for _, slot := range a.EnvSlots() {
		env[slot.EnvVar] = a.TierOf(slot)
	}
	return env
}

// FindReal 跳过 newgate 自己的 shim 查找真实客户端，防止接管后递归启动自身。
func (a *Agent) FindReal(skipDir string) (string, error) {
	skipDir = filepath.Clean(skipDir)
	for _, name := range a.Bin {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if dir == "" || filepath.Clean(dir) == skipDir {
				continue
			}
			path := filepath.Join(dir, name)
			if !isExec(path) {
				continue
			}
			if real, err := filepath.EvalSymlinks(path); err == nil {
				if filepath.Dir(real) == skipDir ||
					strings.HasSuffix(filepath.Base(real), "newgate") {
					continue
				}
			}
			return path, nil
		}
	}
	return "", i18n.E("cannot find {names} in PATH (skipped the shim directory {dir})",
		i18n.A{"names": strings.Join(a.Bin, "/"), "dir": skipDir})
}

func isExec(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
