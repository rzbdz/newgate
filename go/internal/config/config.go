package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/internal/paths"
)

// Roles 是语义档位。opencode / oh-my-openagent 侧只写这些名字，永不写真实模型名。
// 档位是照着 tmp/oh-my-openagent.json 里实际用到的分层定的。
var Roles = []string{"heavy", "mid", "light", "vision"}

const ProxyPort = 8899

// ClassifyModel 把一个真实模型名（可能带 provider/ 前缀）归类到语义档位。
// 这是 docs/11-proxy-native.md §3.4 的档位启发式。命中都会被记录，便于用户核对。
func ClassifyModel(s string) (role string, exact bool) {
	m := strings.ToLower(s)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "opus"), strings.Contains(m, "-sol"):
		return "heavy", true
	case strings.Contains(m, "gemini") && strings.Contains(m, "pro"):
		return "vision", true
	case strings.Contains(m, "sonnet"), strings.Contains(m, "terra"):
		return "mid", true
	case strings.Contains(m, "luna"), strings.Contains(m, "haiku"),
		strings.Contains(m, "flash"), strings.Contains(m, "lite"),
		strings.Contains(m, "mini"), strings.Contains(m, "small"):
		return "light", true
	}
	return "mid", false // 猜不出来归中档，但标记为非精确，调用方要打日志
}

// Provider 一个上游账号。
type Provider struct {
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // 优先于 APIKey
	Protocol  string `json:"protocol,omitempty"`    // openai | anthropic，默认 openai
}

func (p Provider) Key() string {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v
		}
	}
	return p.APIKey
}

type Providers struct {
	Providers map[string]Provider `json:"providers"`
}

// Binding role -> 具体 (provider, model)
type Binding struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Profile 一套档位绑定 + 它在 fallback 链里的位置。
// 可以是**稀疏的**——只定义关心的档位，其余跳到链上下一个 profile。
type Profile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Priority 越小越靠前。不写按 DefaultPriority(50) 算。
	Priority *int `json:"priority,omitempty"`
	// Pinned 「我当链头时，链到我为止」——不好用就报错，别偷偷换。
	Pinned bool `json:"pinned,omitempty"`
	// Excluded 「别人别自动掉到我这」——只能被显式选中。
	Excluded bool                  `json:"excluded,omitempty"`
	Roles    map[string]Candidates `json:"roles"`
	// Fallback 本 profile 内所有未定义档位的兜底，等价于 roles["*"]。
	Fallback *Binding `json:"fallback,omitempty"`
}

func (p *Profile) Prio() int {
	if p.Priority == nil {
		return DefaultPriority
	}
	return *p.Priority
}

// CandidatesFor 返回这个 profile 为某档位提供的候选列表（可能为空 = 稀疏）。
func (p *Profile) CandidatesFor(role string) Candidates {
	if c, ok := p.Roles[role]; ok && len(c) > 0 {
		return c
	}
	if c, ok := p.Roles["*"]; ok && len(c) > 0 {
		return c
	}
	if p.Fallback != nil {
		return Candidates{*p.Fallback}
	}
	return nil
}

// Resolve 取第一个候选。保留给只需要单个绑定的老调用点（status/tui 等）。
func (p *Profile) Resolve(role string) (Binding, bool) {
	c := p.CandidatesFor(role)
	if len(c) == 0 {
		return Binding{}, false
	}
	return c[0], true
}

type State struct {
	ActiveProfile string `json:"active_profile"`
	// FallbackProfile 已废弃：整条 fallback 链现在由 profile 的 priority 决定。
	// 保留字段只为兼容旧配置——若设置了，构链时把它排到链头之后（优先级最高的非链头）。
	FallbackProfile string `json:"fallback_profile,omitempty"`
	// Chain 链的成本上界。
	Chain     ChainLimits `json:"chain"`
	Port      int         `json:"port"`
	TakenOver bool        `json:"taken_over"`
	// Debug 打印每个请求的完整头/体（密钥脱敏）。出错时无论如何都会记全。
	Debug bool `json:"debug"`
	// DebugUntil debug 自动过期时刻（RFC3339）。debug 单条能记 8KB+，
	// 忘了关会把磁盘写满，所以默认只开一段时间。
	DebugUntil string `json:"debug_until,omitempty"`
	// SchemaRepair 给缺 required 的 tool schema 补上 "required": []。
	// 按 JSON Schema 规范这是语义无操作，只为让严格校验器（DeepSeek）通过。
	// 用指针以便区分「没配」和「显式关闭」，默认开。
	SchemaRepair *bool `json:"schema_repair,omitempty"`
}

// ChainLimits 防止一个请求把整条链串一遍（6 家 × 40s = 4 分钟才失败）。
type ChainLimits struct {
	MaxAttempts   int  `json:"max_attempts"` // 实际发出的请求数上限，跨两级累计
	TotalBudgetMs int  `json:"total_budget_ms"`
	FallbackOn400 bool `json:"fallback_on_400"` // 见 docs/18 §6：默认关
}

func (c ChainLimits) Attempts() int {
	if c.MaxAttempts <= 0 {
		return 3
	}
	return c.MaxAttempts
}

func (c ChainLimits) Budget() int {
	if c.TotalBudgetMs <= 0 {
		return 120000
	}
	return c.TotalBudgetMs
}

func (s *State) RepairEnabled() bool {
	return s.SchemaRepair == nil || *s.SchemaRepair
}

func SetSchemaRepair(on bool) error {
	s := LoadState()
	s.SchemaRepair = &on
	return SaveState(s)
}

// DebugActive debug 是否仍在有效期内。过期即视为关闭。
func (s *State) DebugActive() bool {
	if !s.Debug {
		return false
	}
	if s.DebugUntil == "" {
		return true // 未设过期（显式永久开）
	}
	t, err := time.Parse(time.RFC3339, s.DebugUntil)
	if err != nil {
		return true
	}
	return time.Now().Before(t)
}

// SetDebug 开启 debug。ttl>0 时到期自动失效；ttl==0 表示不过期（需手动关）。
func SetDebug(on bool, ttl time.Duration) error {
	s := LoadState()
	s.Debug = on
	s.DebugUntil = ""
	if on && ttl > 0 {
		s.DebugUntil = time.Now().Add(ttl).Format(time.RFC3339)
	}
	return SaveState(s)
}

// ---------- IO ----------

func writeJSON(path string, v interface{}, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := ioutil.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v interface{}) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func LoadProviders() (*Providers, error) {
	p := &Providers{Providers: map[string]Provider{}}
	if err := readJSON(paths.ProvidersFile(), p); err != nil {
		return nil, err
	}
	if p.Providers == nil {
		p.Providers = map[string]Provider{}
	}
	return p, nil
}

func SaveProviders(p *Providers) error {
	return writeJSON(paths.ProvidersFile(), p, 0o600) // 含密钥
}

func LoadProfile(name string) (*Profile, error) {
	var pr Profile
	if err := readJSON(filepath.Join(paths.Mappings(), name+".json"), &pr); err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	return &pr, nil
}

func SaveProfile(pr *Profile) error {
	return writeJSON(filepath.Join(paths.Mappings(), pr.Name+".json"), pr, 0o600)
}

func ListProfiles() ([]string, error) {
	ents, err := ioutil.ReadDir(paths.Mappings())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out, nil
}

func LoadState() *State {
	s := &State{ActiveProfile: "cn", FallbackProfile: "ds", Port: ProxyPort}
	_ = readJSON(paths.StateFile(), s)
	if s.Port == 0 {
		s.Port = ProxyPort
	}
	if s.ActiveProfile == "" {
		s.ActiveProfile = "cn"
	}
	return s
}

func SaveState(s *State) error { return writeJSON(paths.StateFile(), s, 0o600) }

// SetFallbackProfile 设置备用 profile。传空字符串 = 关闭 failover。
func SetFallbackProfile(name string) error {
	s := LoadState()
	if name == "" {
		s.FallbackProfile = ""
		return SaveState(s)
	}
	if _, err := LoadProfile(name); err != nil {
		avail, _ := ListProfiles()
		return fmt.Errorf("profile %q 不存在（可用：%s）", name, strings.Join(avail, ", "))
	}
	s.FallbackProfile = name
	return SaveState(s)
}

// SetActiveProfile 校验存在后写入 state。绝不静默回落。
func SetActiveProfile(name string) error {
	if _, err := LoadProfile(name); err != nil {
		avail, _ := ListProfiles()
		return fmt.Errorf("profile %q 不存在（可用：%s）", name, strings.Join(avail, ", "))
	}
	s := LoadState()
	s.ActiveProfile = name
	return SaveState(s)
}

// Validate 检查 active profile 引用的 provider 都存在、都有 key。
func Validate() []string {
	var problems []string
	provs, err := LoadProviders()
	if err != nil {
		return []string{"providers.json 读不出: " + err.Error()}
	}
	names, _ := ListProfiles()
	for _, n := range names {
		pr, err := LoadProfile(n)
		if err != nil {
			problems = append(problems, fmt.Sprintf("profile %s: %v", n, err))
			continue
		}
		for role, cands := range pr.Roles {
			for _, b := range cands {
				p, ok := provs.Providers[b.Provider]
				if !ok {
					problems = append(problems, fmt.Sprintf("profile %s 档位 %s: provider %q 未定义", n, role, b.Provider))
					continue
				}
				if p.Key() == "" {
					problems = append(problems, fmt.Sprintf("provider %s: 没有 api_key（也没有 api_key_env 指向的环境变量）", b.Provider))
				}
				if b.Model == "" {
					problems = append(problems, fmt.Sprintf("profile %s 档位 %s: model 为空", n, role))
				}
			}
		}
	}
	return problems
}
