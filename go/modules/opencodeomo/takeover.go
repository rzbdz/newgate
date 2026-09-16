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