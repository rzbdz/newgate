// Package store 是 Config Store：磁盘上的单一事实源。
//
// 职责边界（docs/03-architecture.md）：
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
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/roleprov"
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
	// 模块贡献的动态角色键（omo 的 omo-sisyphus / cat-deep …）跟着快照一起
	// 刷新：它们也是配置——键是谁、缺省跟哪一档走，都写在模块自己的文件里。
	// 读失败不挡住加载（失败开放）：那个模块的键退化成「没注册」，
	// 用户在 `newgate omo` / doctor 里能看到原因。
	_ = roleprov.Refresh()

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

// writeJSON 用同目录临时文件 + rename 保证读者只看到完整旧版或新版。
// 当前没有跨进程写锁，因此多个写者并发更新同一文件时仍是最后写入者生效。
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

// LoadProfile 读一个 profile，**含 extends 合并**（变体只写差异项，
// 其余从 base 继承，见 domain.Profile.MergeFrom）。
//
// 文件形态两种：`name.json`（canonical，程序写）或 `name.kv`（手写友好，
// 见 kv.go）。并存时 **.kv 赢**——kv 只会被人刻意创建，它出现就是最新的
// 意图；`newgate profile kv --write` 转换时会把旧 json 改名 .bak。
func LoadProfile(name string) (*domain.Profile, error) {
	return loadProfile(name, map[string]bool{})
}

func loadProfile(name string, loading map[string]bool) (*domain.Profile, error) {
	if loading[name] {
		return nil, fmt.Errorf("extends 成环：%s 绕回了自己", name)
	}
	pr, err := readProfileFile(name)
	if err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	if pr.Extends == "" {
		return pr, nil
	}
	loading[name] = true
	base, err := loadProfile(pr.Extends, loading)
	delete(loading, name)
	if err != nil {
		return nil, fmt.Errorf("extends %q 解析失败: %w", pr.Extends, err)
	}
	pr.MergeFrom(base)
	pr.Extends = "" // 已合并，解析后的视图不再背 extends 声明
	return pr, nil
}

func readProfileFile(name string) (*domain.Profile, error) {
	if b, err := ioutil.ReadFile(filepath.Join(paths.Mappings(), name+".kv")); err == nil {
		return ParseProfileKV(string(b))
	}
	var pr domain.Profile
	if err := readJSON(filepath.Join(paths.Mappings(), name+".json"), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// LoadProfileRaw 读**未合并**的原始 profile（extends 声明原样保留）。
// 给编辑/转换用：对一个紧凑的派生声明改一个字段，不该把它展开成全量。
func LoadProfileRaw(name string) (*domain.Profile, error) {
	pr, err := readProfileFile(name)
	if err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	return pr, nil
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
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		var n string
		switch {
		case strings.HasSuffix(e.Name(), ".json"):
			n = strings.TrimSuffix(e.Name(), ".json")
		case strings.HasSuffix(e.Name(), ".kv"):
			n = strings.TrimSuffix(e.Name(), ".kv")
		default:
			continue
		}
		// 同名 json/kv 只出一个名字（谁赢见 LoadProfile）
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---------- state ----------

func LoadState() *domain.State {
	s := &domain.State{}
	if b, err := ioutil.ReadFile(paths.StateFile()); err == nil {
		if json.Unmarshal(b, s) == nil {
			var fields map[string]json.RawMessage
			if json.Unmarshal(b, &fields) == nil {
				known := stateJSONFields()
				s.ModuleConfig = make(map[string][]byte)
				for name, raw := range fields {
					if !known[name] {
						s.ModuleConfig[name] = append([]byte(nil), raw...)
					}
				}
			}
		}
	}
	s.Normalize()
	return s
}