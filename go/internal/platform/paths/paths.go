package paths

import (
	"os"
	"path/filepath"
)

// Home 目录下的 pid / lock（按需求：一旦 start 就在 ~ 创建）
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}

func root() string {
	if v := os.Getenv("NEWGATE_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "newgate")
	}
	return filepath.Join(Home(), ".config", "newgate")
}

func Config() string        { return root() }
func Mappings() string      { return filepath.Join(root(), "mappings") }
func ProvidersFile() string { return filepath.Join(root(), "providers.json") }
func StateFile() string     { return filepath.Join(root(), "state.json") }
func BackupDir() string     { return filepath.Join(root(), "backups") }
func LogFile() string       { return filepath.Join(root(), "newgate.log") }

// pid / lock 放 $HOME，除非 NEWGATE_HOME 被覆盖（测试时隔离）
func runtimeDir() string {
	if v := os.Getenv("NEWGATE_HOME"); v != "" {
		return v
	}
	return Home()
}

func PidFile() string  { return filepath.Join(runtimeDir(), ".newgate.pid") }
func LockFile() string { return filepath.Join(runtimeDir(), ".newgate.lock") }

// TargetFiles 被接管的目标配置文件。
// NEWGATE_TARGET_DIR 可整体重定向（mock/测试用）。
// 否则在若干候选位置里探测——oh-my-openagent 是 opencode 插件，
// 它的配置可能和 opencode.json 同目录，也可能自己一个目录。
func TargetFiles() []string {
	if d := os.Getenv("NEWGATE_TARGET_DIR"); d != "" {
		return []string{
			filepath.Join(d, "opencode.json"),
			filepath.Join(d, "oh-my-openagent.json"),
		}
	}
	h := Home()
	cfgHome := filepath.Join(h, ".config")
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		cfgHome = v
	}

	out := []string{filepath.Join(cfgHome, "opencode", "opencode.json")}

	// oh-my-openagent.json 的候选位置，取第一个存在的
	for _, c := range []string{
		filepath.Join(cfgHome, "opencode", "oh-my-openagent.json"),
		filepath.Join(cfgHome, "oh-my-openagent", "oh-my-openagent.json"),
		filepath.Join(h, ".oh-my-openagent.json"),
	} {
		if _, err := os.Stat(c); err == nil {
			out = append(out, c)
			return out
		}
	}
	// 一个都没有：返回最可能的那个，让 status/doctor 报「文件不存在」
	out = append(out, filepath.Join(cfgHome, "opencode", "oh-my-openagent.json"))
	return out
}

func EnsureDirs() error {
	for _, d := range []string{Config(), Mappings(), BackupDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
