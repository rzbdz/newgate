// Package store 是 Config Store：磁盘上的单一事实源。
//
// 职责边界（docs/02-architecture.md §2）：
//   - core 只有类型和纯逻辑，一行 IO 都没有
//   - store 负责所有读写、原子替换、密钥的环境变量查找、schema 校验
//   - 上层拿到的是**不可变快照**，避免边解析边读文件带来的竞态
package store

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
)

// Snapshot 一次性读齐的配置快照。解析阶段只看它，不再回头读盘。
type Snapshot struct {
	Providers *domain.Providers
	Profiles  []*domain.Profile
	State     *domain.State
}

// Load 读取完整快照。坏掉的 profile 文件跳过而不是让整体失败——
// 一个手改坏的文件不该让所有 agent 停摆；doctor 会报出来。
func Load() (*Snapshot, error) {
	provs, err := LoadProviders()
	if err != nil {
		return nil, err
	}
	names, err := ListProfiles()
	if err != nil {
		return nil, err
	}
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	return &Snapshot{Providers: provs, Profiles: ps, State: LoadState()}, nil
}

// ---------- 原子写 ----------

// TODO(M2): 加 flock。现在靠「同目录 rename 原子」保证读到的一定是
// 完整的旧版或新版，但并发写仍可能丢更新（docs/06 §7）。
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

// ---------- providers ----------

// LoadProviders 读 provider 表，并把 api_key_env 指向的环境变量解析成明文。
//
// 这个解析放在 store 而不是 core：读环境变量是 IO，core 必须保持纯净
// （domain.Provider.Key() 因此只返回 APIKey 字段）。
func LoadProviders() (*domain.Providers, error) {
	p := &domain.Providers{Providers: map[string]domain.Provider{}}
	if err := readJSON(paths.ProvidersFile(), p); err != nil {
		return nil, err
	}
	if p.Providers == nil {
		p.Providers = map[string]domain.Provider{}
	}
	for name, prov := range p.Providers {
		if prov.APIKey == "" && prov.APIKeyEnv != "" {
			if v := os.Getenv(prov.APIKeyEnv); v != "" {
				prov.APIKey = v
				p.Providers[name] = prov
			}
		}
	}
	return p, nil
}

// SaveProviders 写 provider 表。
//
// 0660 而不是 0600：共享部署模型（同一台机器、developer 组的多用户管理
// 同一个 newgate，见 docs/03）里组员要能读它——`newgate status` / wrapper
// 都要解析 provider。组边界就是信任边界；单用户部署想收紧，chmod 0600
// providers.json 即可，代码不会把它改回去（只在写入时用这个 mode）。
// 注意：不能把 LoadProviders 解析出来的环境变量值写回去，否则密钥就落盘了。
// 调用方必须传入原始形态。
func SaveProviders(p *domain.Providers) error {
	return writeJSON(paths.ProvidersFile(), p, 0o660)
}

// ---------- profiles ----------

func LoadProfile(name string) (*domain.Profile, error) {
	var pr domain.Profile
	if err := readJSON(filepath.Join(paths.Mappings(), name+".json"), &pr); err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	return &pr, nil
}

// SaveProfile 写 profile。不含密钥（只有 provider/model 绑定），组内共享读
// 没有暴露面：wrapper 启动（launch.Launch）本就要解析它来注入真实模型名。
func SaveProfile(pr *domain.Profile) error {
	return writeJSON(filepath.Join(paths.Mappings(), pr.Name+".json"), pr, 0o660)
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

// ---------- state ----------

func LoadState() *domain.State {
	s := &domain.State{}
	_ = readJSON(paths.StateFile(), s)
	s.Normalize()
	return s
}

func SaveState(s *domain.State) error { return writeJSON(paths.StateFile(), s, 0o660) }

// NewControlToken 生成控制端点令牌（crypto/rand，48 位十六进制）。
func NewControlToken() string {
	b := make([]byte, 24)
	if _, err := crand.Read(b); err != nil {
		// rand 失败几乎只在早期 boot 的虚拟机里发生；退化到时间熵也比
		// 空令牌（= 端点永远 403）好
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// EnsureControlToken 幂等地保证 state.json 里有控制令牌，返回最新 state。
//
// 必须在 daemon.Spawn **之前**调用（cmdStart / launch.Launch / Serve 各自
// 兜一次底）：令牌先落盘，daemon 起来时 watcher 的初次加载就能读到，
// 不存在「daemon 拿着空令牌跑着」的窗口。之后所有 LoadState→SaveState
// 的调用方（--set-profile 等）都从盘上重新读，令牌不会被冲掉。
func EnsureControlToken() *domain.State {
	s := LoadState()
	if s.ControlToken == "" {
		s.ControlToken = NewControlToken()
		_ = SaveState(s)
	}
	return s
}

// SetActiveProfile 设置某个 agent 的链头。agent 为空 = 设全局默认。
//
// 校验存在后才写，**绝不静默回落**——那正是「切了没生效」这类故障的来源。
func SetActiveProfile(agent, profile string) error {
	if _, err := LoadProfile(profile); err != nil {
		avail, _ := ListProfiles()
		return fmt.Errorf("profile %q 不存在（可用：%s）", profile, strings.Join(avail, ", "))
	}
	s := LoadState()
	if agent == "" {
		s.DefaultProfile = profile
	} else {
		if s.Active == nil {
			s.Active = map[string]string{}
		}
		s.Active[agent] = profile
	}
	return SaveState(s)
}

// ClearActiveProfile 让某个 agent 回落到全局默认。
func ClearActiveProfile(agent string) error {
	s := LoadState()
	delete(s.Active, agent)
	return SaveState(s)
}

func SetDebug(on bool, untilRFC3339 string) error {
	s := LoadState()
	s.Debug = on
	s.DebugUntil = ""
	if on {
		s.DebugUntil = untilRFC3339
	}
	return SaveState(s)
}

func SetSchemaRepair(on bool) error {
	s := LoadState()
	s.SchemaRepair = &on
	return SaveState(s)
}

// SetTakeoverWanted 记下「用户要不要接管这个 agent」（期望态）。
// 只改期望态，不碰磁盘——具体怎么接管是 runtime/takeover 的事。
func SetTakeoverWanted(agent string, want bool) error {
	s := LoadState()
	if s.Takeover == nil {
		s.Takeover = map[string]bool{}
	}
	if want {
		// 默认就是想接管，所以「想要」用删除条目表示，state.json 保持干净
		delete(s.Takeover, agent)
	} else {
		s.Takeover[agent] = false
	}
	if len(s.Takeover) == 0 {
		s.Takeover = nil
	}
	return SaveState(s)
}

// SetSpecialTreatment 开关整个 special_treatment 层。
func SetSpecialTreatment(on bool) error {
	s := LoadState()
	s.SpecialTreatment = &on
	return SaveState(s)
}

// SetSpecialPlugin 单独开关一个插件。关 = 记进 SpecialOff，开 = 从里面删掉。
func SetSpecialPlugin(name string, on bool) error {
	s := LoadState()
	out := make([]string, 0, len(s.SpecialOff))
	for _, n := range s.SpecialOff {
		if n != name {
			out = append(out, n)
		}
	}
	if !on {
		out = append(out, name)
	}
	if len(out) == 0 {
		out = nil
	}
	s.SpecialOff = out
	return SaveState(s)
}

// ---------- 校验 ----------

// Validate 检查所有 profile 引用的 provider 都存在、都有 key。
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
		for tier, cands := range pr.Roles {
			for _, b := range cands {
				p, ok := provs.Providers[b.Provider]
				if !ok {
					problems = append(problems,
						fmt.Sprintf("profile %s 档位 %s: provider %q 未定义", n, tier, b.Provider))
					continue
				}
				if p.Key() == "" {
					hint := "providers.json 里填 api_key"
					if p.APIKeyEnv != "" {
						hint = "设环境变量 " + p.APIKeyEnv + " 或填 api_key"
					}
					problems = append(problems,
						fmt.Sprintf("provider %s 没有 key → %s", b.Provider, hint))
				}
				if b.Model == "" {
					problems = append(problems,
						fmt.Sprintf("profile %s 档位 %s: model 为空", n, tier))
				}
			}
		}
	}
	return problems
}

// TODO(M2): schema 校验 + 版本迁移。
// 所有配置文件带 schemaVersion，校验失败要报错并指出具体路径，
// 绝不静默忽略字段（docs/06 §8）。
