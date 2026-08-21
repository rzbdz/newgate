package injection

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

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
)

const ProviderID = "newgate"

// Report 记录一次接管做了什么，给用户看。
type Report struct {
	File     string
	Rewrites []string // "openai-relay/model-large -> newgate/heavy (精确)"
	Fuzzy    int
	Skipped  string
}

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

func writeAtomic(path string, b []byte) error {
	tmp := path + ".newgate.tmp"
	if err := ioutil.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func marshal(v interface{}) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ---------- opencode.json ----------

// buildNewgateProvider 生成指向本地代理的 provider 块。
// 模型名就是语义档位——这是 docs/16 的核心：配置里不写真实模型名。
func buildNewgateProvider(port int) map[string]interface{} {
	models := map[string]interface{}{}
	for _, r := range domain.Roles {
		models[r] = map[string]interface{}{
			"name": "newgate " + r,
			"limit": map[string]interface{}{
				"context": 1000000,
				"output":  64000,
			},
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
func ApplyOpencode(target string, port int) (*Report, error) {
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
	np, err := marshal(buildNewgateProvider(port))
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
	root["model"] = json.RawMessage(`"` + ProviderID + `/heavy"`)
	root["small_model"] = json.RawMessage(`"` + ProviderID + `/light"`)
	rep.Rewrites = append(rep.Rewrites,
		"model -> "+ProviderID+"/heavy",
		"small_model -> "+ProviderID+"/light",
		"provider."+ProviderID+" 已注入（原有 provider 全部保留）")

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}

// ---------- oh-my-openagent.json ----------

// ApplyOpenagent 把所有 "model": "provider/real-model" 改写成 "newgate/<role>"，
// 结构（agents / categories / fallback_models / variant）完全保留。
func ApplyOpenagent(target string, port int) (*Report, error) {
	rep := &Report{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return rep, err
	}
	if err := backup(target); err != nil {
		return rep, err
	}

	var root interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return rep, fmt.Errorf("解析 %s 失败: %w", target, err)
	}

	walkModels(root, func(old string) string {
		if strings.HasPrefix(old, ProviderID+"/") {
			return old // 已接管，幂等
		}
		role, exact := ClassifyModel(old)
		tag := "精确"
		if !exact {
			tag = "模糊-未命中规则"
			rep.Fuzzy++
		}
		rep.Rewrites = append(rep.Rewrites,
			fmt.Sprintf("%s -> %s/%s (%s)", old, ProviderID, role, tag))
		return ProviderID + "/" + role
	})

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}

// walkModels 递归改写所有 "model" 字符串字段。
func walkModels(node interface{}, fn func(string) string) {
	switch n := node.(type) {
	case map[string]interface{}:
		for k, v := range n {
			if k == "model" {
				if s, ok := v.(string); ok && s != "" {
					n[k] = fn(s)
					continue
				}
			}
			walkModels(v, fn)
		}
	case []interface{}:
		for _, v := range n {
			walkModels(v, fn)
		}
	}
}

// ---------- 编排 ----------

func IsTakenOver(target string) bool {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `"`+ProviderID+`/`) ||
		strings.Contains(string(b), `"`+ProviderID+`"`)
}

// ApplyAll 接管所有目标文件。找不到的文件跳过并记录，不视为错误。
func ApplyAll(port int) ([]*Report, error) {
	var reps []*Report
	for _, t := range paths.TargetFiles() {
		if _, err := os.Stat(t); os.IsNotExist(err) {
			reps = append(reps, &Report{File: t, Skipped: "文件不存在"})
			continue
		}
		var rep *Report
		var err error
		if strings.Contains(filepath.Base(t), "openagent") {
			rep, err = ApplyOpenagent(t, port)
		} else {
			rep, err = ApplyOpencode(t, port)
		}
		if err != nil {
			return reps, fmt.Errorf("接管 %s 失败: %w", t, err)
		}
		reps = append(reps, rep)
	}
	return reps, nil
}

// RestoreAll 从 original/ 还原。这是逃生舱：还原后删掉 newgate 也能正常用。
func RestoreAll() ([]string, error) {
	var done []string
	for _, t := range paths.TargetFiles() {
		orig := originalPath(t)
		b, err := ioutil.ReadFile(orig)
		if err != nil {
			continue // 没备份说明没接管过
		}
		if err := writeAtomic(t, b); err != nil {
			return done, fmt.Errorf("还原 %s 失败: %w", t, err)
		}
		done = append(done, t)
	}
	return done, nil
}
