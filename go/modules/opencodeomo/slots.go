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