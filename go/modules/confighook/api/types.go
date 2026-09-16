package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Slot 描述客户端的一个模型槽位如何映射到 newgate 语义档位。
// EnvVar 为空的槽位仍可供配置接管使用，但不会参与进程环境注入。
type Slot struct {
	Name   string
	Tier   string
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
		env[slot.EnvVar] = slot.Tier
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
	return "", fmt.Errorf("PATH 里找不到 %s（已跳过 shim 目录 %s）",
		strings.Join(a.Bin, "/"), skipDir)
}

func isExec(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
