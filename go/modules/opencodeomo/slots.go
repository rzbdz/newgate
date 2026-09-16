package opencodeomo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/roleprov"
)

// omo 槽位：opencode 插件 oh-my-openagent 的 intra-agent 键体系。
//
// 为什么不是「把每个槽位的模型名按体格翻译成档位」就完了
//
// 老做法（ClassifyModel）把 `agents.sisyphus.model` 直接换成 `newgate/normal`。
// 两条信息在这句话里被**丢掉**了：
//
//	其一，槽位身份。sisyphus 和 librarian 都映射到 normal 之后，配置文件里
//	  再也看不出谁是谁；用户想「sisyphus 用贵的、librarian 用便宜的」时，
//	  没有地方可以写。
//	其二，用户的意图。`gpt-5.6-sol` 加 `variant: max` 和 `claude-sonnet-5`
//	  裸跑，在老做法里可能落到同一档，可它们想表达的强度不是一回事。
//
// 新做法：接管时给每个槽位分配一个**稳定键**（omo-sisyphus / cat-deep），
// 把「这个键现在跟谁走」写进 omo-slots.json。于是
//
//	配置文件里留下的是身份（newgate/omo-sisyphus），不是体格；
//	键 → 档位/链的映射变成用户可改的一行配置（profile 里写
//	  `"omo-sisyphus": ["@normal", "smt/terra"]` 就覆盖了缺省）；
//	core 不需要认识 omo——它只看到一批动态角色键（domain.ExtraRole）。
//
// 命名与缺省由本文件（omo 模块）决定，框架只提供机制。
const (
	// OmoAgentPrefix intra-agent（agents.<名>）的键前缀。
	OmoAgentPrefix = "omo-"
	// OmoCatPrefix 任务类别（categories.<名>）的键前缀。
	OmoCatPrefix = "cat-"
)

// Slot 一个 omo 槽位在接管前的原貌。
type Slot struct {
	Kind      string // agent | category
	Name      string
	Model     string
	Variant   string
	Fallbacks []string // 仅模型名（variant 不参与——链交给 newgate 之后它没意义了）
	// FallbackObjects fallback_models 的元素形态是不是对象（{"model":…,"variant":…}）。
	// 改写时按原形态写回，别把 omo 认得的配置写成它不认的样子。
	FallbackObjects bool
}

// Key 这个槽位在 newgate 里的键名。
func (s Slot) Key() string {
	if s.Kind == "category" {
		return OmoCatPrefix + s.Name
	}
	return OmoAgentPrefix + s.Name
}

// ---------- 读原文件 ----------

// DiscoverSlots 读出 oh-my-openagent.json 里所有槽位。
//
// 纯读，不改写：接管前用它算键名（好把模型 id 提前注册进 opencode.json 的
// provider 块），接管后用它对齐已有键。
//
// 解析失败**必须报错**：这里一旦静默返回空，接管就变成「报告 0 处改写、
// 配置原样不动」——看起来成功了，其实什么都没干（2026-09-16 真踩过：
// fallback_models 的元素是对象而不是字符串，整份文件解析失败）。
func DiscoverSlots(target string) ([]Slot, error) {
	if target == "" {
		return nil, nil
	}
	b, err := ioutil.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var root struct {
		Agents     map[string]omoNode `json:"agents"`
		Categories map[string]omoNode `json:"categories"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("%s 解析失败: %w", filepath.Base(target), err)
	}
	var out []Slot
	for _, kind := range []string{"agent", "category"} {
		m := root.Agents
		if kind == "category" {
			m = root.Categories
		}
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names) // 键名顺序稳定，报告与注册表才不会每次都跳
		for _, n := range names {
			node := m[n]
			if node.Model == "" {
				continue
			}
			sl := Slot{Kind: kind, Name: n, Model: node.Model, Variant: node.Variant}
			for i, f := range node.FallbackModels {
				if f.Model == "" {
					continue
				}
				sl.Fallbacks = append(sl.Fallbacks, f.Model)
				if i == 0 {
					sl.FallbackObjects = f.fromObject
				}
			}
			out = append(out, sl)
		}
	}
	return out, nil
}

type omoNode struct {
	Model          string   `json:"model"`
	Variant        string   `json:"variant"`
	FallbackModels []omoRef `json:"fallback_models"`
}

// omoRef fallback_models 的一个元素。omo 两种形态都在用：
//
//	"smt-codex/gpt-5.6-terra"                       裸模型名
//	{"model": "smt-codex/gpt-5.6-terra", "variant": "medium"}   带强度
//
// 两种都要收，所以自己解——写成 []string 会让整个文件解析失败，
// 而解析失败的表现是「接管报告说 0 处改写」，最难查的那种静默。
type omoRef struct {
	Model      string `json:"model"`
	Variant    string `json:"variant,omitempty"`
	fromObject bool
}

func (r *omoRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.Model)
	}
	type alias omoRef // 防递归
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*r = omoRef(a)
	r.fromObject = true
	return nil
}

// OriginalSlots 从 backups/original/ 里的原始文件读槽位。
//
// 用来在**重新接管**时找回 `was`：用户第二次 `newgate on opencode` 时，
// 磁盘上的文件早就被我们改写成 newgate/xxx 了，只有备份还记得它原来是什么
// 模型、什么 variant。没有这份记忆，建议值就无从算起。
func OriginalSlots() []Slot {
	slots, _ := DiscoverSlots(originalPath(omoTargetPath()))
	return slots
}

func omoTargetPath() string {
	for _, t := range TargetFiles() {
		if strings.Contains(filepath.Base(t), "openagent") {
			return t
		}
	}
	return ""
}

// ---------- 注册表 ----------

// OmoSlots 槽位登记表（~/.config/newgate/omo-slots.json）。
type OmoSlots struct {
	Version   int               `json:"version"`
	Source    string            `json:"source"`
	Updated   string            `json:"updated,omitempty"`
	Mode      string            `json:"mode,omitempty"`      // current（现状，默认）| suggested（建议）
	Overrides map[string]string `json:"overrides,omitempty"` // 键 → 档位名/@引用，最高优先
	Slots     []OmoSlot         `json:"slots"`
	Note      string            `json:"note,omitempty"`
}

// OmoSlot 一个槽位的登记项。
type OmoSlot struct {
	Key          string   `json:"key"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Was          string   `json:"was,omitempty"` // 接管前的真实模型名
	WasFallbacks []string `json:"was_fallbacks,omitempty"`
	Variant      string   `json:"variant,omitempty"`
	Current      string   `json:"current"`             // 现状：接管时它的体格
	Suggested    string   `json:"suggested,omitempty"` // 建议：体格 + variant 强度
	Default      string   `json:"default"`             // 实际生效（overrides > mode 决定的）
	Why          string   `json:"why,omitempty"`
}

// ReadOmoSlots 读注册表。没有（没接管过 omo）返回 nil，不是错误。
func ReadOmoSlots() *OmoSlots {
	var s OmoSlots
	b, err := ioutil.ReadFile(SlotsFile())
	if err != nil || len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	return &s
}

// SlotBinding 一个槽位**实际生效**的缺省绑定（overrides > mode > current）。
// 返回值直接写进 domain.ExtraRole.Default，支持 "@引用" / "provider/model" / 档位名。
func (s *OmoSlots) SlotBinding(key string) string {
	if s == nil {
		return ""
	}
	if v, ok := s.Overrides[key]; ok && v != "" {
		return v
	}
	for _, sl := range s.Slots {
		if sl.Key != key {
			continue
		}
		if s.Mode == "suggested" && sl.Suggested != "" {
			return sl.Suggested
		}
		return sl.Current
	}
	return ""
}

// SlotOf 按键查登记项。
func (s *OmoSlots) SlotOf(key string) (OmoSlot, bool) {
	if s == nil {
		return OmoSlot{}, false
	}
	for _, sl := range s.Slots {
		if sl.Key == key {
			return sl, true
		}
	}
	return OmoSlot{}, false
}