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
	"github.com/rzbdz/newgate/go/modules/config/paths"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
)

const ProviderID = "newgate"

// Report 记录一次接管做了什么，给用户看。
type Report = agentapi.TakeoverReport

func originalPath(target string) string {
	return filepath.Join(paths.BackupDir(), "original", filepath.Base(target))
}

// backup 首次接管时把原文件存成 original/（用于 stop 还原），
// 同时每次都存一份带时间戳的历史。
//
// 关键防护：original/ 只允许写「未被接管」的内容。
// 否则一旦 original/ 被误删，下次 start 就会把已接管的文件当成"原始"存进去，
// 之后 stop 还原出来的就是被污染的版本——用户的原配置永久丢失。
func backup(target string) error {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return err
	}
	tainted := bytes.Contains(b, []byte(`"`+ProviderID+`"`)) ||
		bytes.Contains(b, []byte(`"`+ProviderID+`/`))

	orig := originalPath(target)
	if err := os.MkdirAll(filepath.Dir(orig), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(orig); os.IsNotExist(err) {
		if tainted {
			return fmt.Errorf(
				"拒绝备份：%s 已经被 newgate 接管过，但 %s 不存在。\n"+
					"  直接拿它当原始备份会让你的原配置永久丢失。\n"+
					"  要么从 backups/<时间戳>/ 里找一份干净的放回 original/，\n"+
					"  要么手工把配置改回去后再 newgate start",
				filepath.Base(target), orig)
		}
		if err := ioutil.WriteFile(orig, b, 0o600); err != nil {
			return err
		}
	}

	ts := filepath.Join(paths.BackupDir(), time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(ts, 0o700); err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(ts, filepath.Base(target)), b, 0o600); err != nil {
		return err
	}
	pruneSnapshots(10)
	return nil
}

// pruneSnapshots 只保留最近 keep 份时间戳快照，别让备份目录无限长。
// original/ 永不删。
func pruneSnapshots(keep int) {
	ents, err := ioutil.ReadDir(paths.BackupDir())
	if err != nil {
		return
	}
	var stamps []string
	for _, e := range ents {
		if e.IsDir() && e.Name() != "original" {
			stamps = append(stamps, e.Name())
		}
	}
	if len(stamps) <= keep {
		return
	}
	sort.Strings(stamps) // 时间戳格式可直接字典序排序
	for _, s := range stamps[:len(stamps)-keep] {
		_ = os.RemoveAll(filepath.Join(paths.BackupDir(), s))
	}
}

func writeAtomic(path string, b []byte) error { return writeAtomicMode(path, b, 0o600) }

func marshal(v interface{}) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ---------- opencode.json ----------

// buildNewgateProvider 生成指向本地代理的 provider 块。
// 模型名就是语义档位——这是 docs/01-product.md 的核心：配置里不写真实模型名。
//
// extra 是模块槽位键（omo-sisyphus / cat-deep …）：omo 的配置文件会引用
// 这些 id，得先在 provider 的 models 里登记出来，否则 opencode 认不出这个模型。
// 键从哪来由模块决定（见 omo.go），这里不做任何 omo 相关的判断。
func buildNewgateProvider(port int, extra []string) map[string]interface{} {
	models := map[string]interface{}{}
	add := func(id string) {
		models[id] = map[string]interface{}{
			"name": "newgate " + id,
			"limit": map[string]interface{}{
				"context": 1000000,
				"output":  64000,
			},
		}
	}
	for _, r := range domain.Roles {
		add(r)
	}
	for _, k := range extra {
		if k != "" && !domain.IsRole(k) {
			add(k)
		}
	}
	return map[string]interface{}{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "newgate (local proxy)",
		"options": map[string]interface{}{
			"baseURL": fmt.Sprintf("http://127.0.0.1:%d/v1", port),
			"apiKey":  "newgate-local",
		},
		"models": models,
	}
}

// ApplyOpencode 合并式改写：只增加 provider.newgate 并把 model/small_model 指过去，
// 用户原有的 provider 块、plugin、以及任何其它键全部原样保留。
func ApplyOpencode(target string, port int, extra []string) (*Report, error) {
	rep := &Report{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return rep, err
	}
	if err := backup(target); err != nil {
		return rep, err
	}

	// 用 map 保留未知键
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return rep, fmt.Errorf("解析 %s 失败: %w", target, err)
	}

	provs := map[string]json.RawMessage{}
	if v, ok := root["provider"]; ok {
		if err := json.Unmarshal(v, &provs); err != nil {
			return rep, fmt.Errorf("provider 字段不是对象: %w", err)
		}
	}
	np, err := marshal(buildNewgateProvider(port, extra))
	if err != nil {
		return rep, err
	}
	provs[ProviderID] = json.RawMessage(np)
	if b, err := marshal(provs); err == nil {
		root["provider"] = json.RawMessage(b)
	} else {
		return rep, err
	}

	// 顶层 model / small_model 指向语义档位
	root["model"] = json.RawMessage(`"` + ProviderID + `/normal"`)
	root["small_model"] = json.RawMessage(`"` + ProviderID + `/light"`)
	rep.Rewrites = append(rep.Rewrites,
		"model -> "+ProviderID+"/normal",
		"small_model -> "+ProviderID+"/light",
		"provider."+ProviderID+" 已注入（原有 provider 全部保留）")

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}

// ---------- oh-my-openagent.json ----------

// ApplyOpenagent 把 omo 的 intra-agent 槽位接到 newgate 上。
//
// 与老版本的区别：**保留槽位身份**。以前每个 `"model": "厂商/模型"` 都被换成
// 按体格算出来的 `newgate/normal`，sisyphus 和 librarian 从此长得一模一样；
// 现在换成按槽位起的键 `newgate/omo-sisyphus` / `newgate/cat-deep`，体格
// （它现在算哪一档、建议算哪一档）记在同目录的 omo-slots.json 里。
//
// 三件事，每一件都不改写用户看不出来的东西：
//
//	槽位键：agents.<名> → omo-<名>，categories.<名> → cat-<名>（omo.go）
//	注册表：键 → 现状档位 / 建议档位 / 原模型名，写 omo-slots.json
//	链的归属：注册表里的 default 就是这个键在 profile 里没写时的缺省
//
// `fallback_models` 收敛成一条（同一个键）：链已经由 newgate 在服务端兜底，
// 留在配置里的多条 fallback 只会让一次失败重试同一件事。原列表记进注册表
// （was_fallbacks），原文件也还在 backups/original/。
//
// 幂等：已经是 newgate/xxx 的槽位不会被再翻译一遍；重新接管时用注册表和
// 备份里的原始文件找回「原来是什么模型」，所以反复接管不会丢信息。
func ApplyOpenagent(target string, port int) (*Report, error) {
	rep := &Report{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return rep, err
	}
	if err := backup(target); err != nil {
		return rep, err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return rep, fmt.Errorf("解析 %s 失败: %w", target, err)
	}

	slots, err := DiscoverSlots(target)
	if err != nil {
		return rep, err
	}
	prev := ReadOmoSlots()
	orig := map[string]Slot{}
	for _, s := range OriginalSlots() {
		orig[s.Kind+"/"+s.Name] = s
	}

	reg := &OmoSlots{}
	if prev != nil {
		reg.Mode = prev.Mode
		reg.Overrides = prev.Overrides
	}
	byKey := map[string]OmoSlot{}
	for _, sl := range slots {
		key := sl.Key()
		origModel, origVariant := "", ""
		if o, ok := orig[sl.Kind+"/"+sl.Name]; ok {
			origModel, origVariant = o.Model, o.Variant
		}

		// was = 接管前的真实模型名。三处信息按可信度排序：备份里的原始文件
		// （从没被我们碰过）> 上次接管记下的 > 当前文件（只有第一次接管时
		// 它还是真名字，之后就是我们写的 newgate/xxx 了）。
		was := ""
		if origModel != "" && !isTakenOver(origModel) {
			was = origModel
		} else {
			if p, ok := prev.SlotOf(key); ok {
				was = p.Was
			}
			if was == "" && !isTakenOver(sl.Model) {
				was = sl.Model
			}
		}

		variant := firstNonEmpty(sl.Variant, origVariant)
		current, note := currentTier(sl.Model, key, prev, was)
		suggested, swhy := Suggest(was, variant)
		entry := OmoSlot{
			Key: key, Kind: sl.Kind, Name: sl.Name,
			Was: was, WasFallbacks: sl.Fallbacks, Variant: variant,
			Current: current, Suggested: suggested, Default: current,
			Why: joinWhy(note, swhy),
		}
		reg.Slots = append(reg.Slots, entry)
		byKey[key] = entry
		rep.Rewrites = append(rep.Rewrites, fmt.Sprintf("%s.%s → %s/%s（现状 %s%s）",
			sl.Kind+"s", sl.Name, ProviderID, key, current, suggestTag(suggested, current)))
	}
	// 注册表里的 default 以 overrides/mode 为准（写文件的人看得见最终归属）
	for i := range reg.Slots {
		reg.Slots[i].Default = reg.SlotBinding(reg.Slots[i].Key)
		if reg.Slots[i].Default == "" {
			reg.Slots[i].Default = reg.Slots[i].Current
		}
		byKey[reg.Slots[i].Key] = reg.Slots[i]
	}
	if err := WriteOmoSlots(reg); err != nil {
		return rep, fmt.Errorf("写槽位注册表失败: %w", err)
	}
	rep.Rewrites = append(rep.Rewrites,
		fmt.Sprintf("槽位注册表 → %s（%d 个键，default 决定每个键跟哪一档走）",
			SlotsFile(), len(reg.Slots)))

	// 改写：只动 agents.*.model / categories.*.model 和它们的 fallback_models
	fallbackIsObject := map[string]bool{}
	for _, s := range slots {
		fallbackIsObject[s.Key()] = s.FallbackObjects
	}
	for kind, plural := range map[string]string{"agent": "agents", "category": "categories"} {
		section, ok := root[plural].(map[string]interface{})
		if !ok {
			continue
		}
		for name, rawNode := range section {
			node, ok := rawNode.(map[string]interface{})
			if !ok {
				continue
			}
			key, ok := byKey[slotKey(kind, name)]
			if !ok {
				continue // DiscoverSlots 跳过的（没有 model 字段）
			}
			node["model"] = ProviderID + "/" + key.Key
			if _, has := node["fallback_models"]; has {
				// 原样保留元素形态（对象 or 字符串），只把内容收敛成同一个键。
				if fallbackIsObject[key.Key] {
					node["fallback_models"] = []interface{}{
						map[string]interface{}{"model": ProviderID + "/" + key.Key}}
				} else {
					node["fallback_models"] = []interface{}{ProviderID + "/" + key.Key}
				}
			}
		}
	}

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}