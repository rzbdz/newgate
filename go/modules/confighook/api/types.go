package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Slot struct {
	Name   string
	Tier   string
	EnvVar string
	Desc   string
}

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

type TakeoverReport struct {
	File     string
	Rewrites []string
	Fuzzy    int
	Skipped  string
}

type ConfigTakeover interface {
	Targets() []string
	IsTakenOver(target string) bool
	Apply(port int) ([]*TakeoverReport, error)
	Restore() ([]string, error)
}

func (a *Agent) EnvSlots() []Slot {
	var out []Slot
	for _, slot := range a.Slots {
		if slot.EnvVar != "" {
			out = append(out, slot)
		}
	}
	return out
}

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
