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

// ThinkCacheFile thinkcache 的落盘冷层文件：滚动 100MB，撑过 daemon 重启。
// 内容是推理原文（= 对话内容），和 dump 一样是明文，权限 0660 随目录的
// 组共享模型走（developer 组内可见）。
func ThinkCacheFile() string { return filepath.Join(root(), "thinkcache.bin") }

// pid / lock 放共享配置目录，便于同一组的多用户管理同一个 daemon。
// NEWGATE_HOME 仍可用于测试时隔离整套运行时文件。
func runtimeDir() string {
	return root()
}

func PidFile() string  { return filepath.Join(runtimeDir(), ".newgate.pid") }
func LockFile() string { return filepath.Join(runtimeDir(), ".newgate.lock") }

// LegacyPidFile / LegacyLockFile 老版本的 pid / lock 位置（$HOME 根下）。
// 只在真实部署（没设 NEWGATE_HOME）时兜底**读**它：升级到共享目录后，
// 正在跑的老 daemon 还把 pid 写在那儿——不兜底的话它就「失踪」了，
// stop 不到、start 又撞端口。
// 沙箱里必须返回空：绝不能让测试里的 stop 顺藤摸瓜摸到真实 daemon。
func LegacyPidFile() string {
	if os.Getenv("NEWGATE_HOME") != "" {
		return ""
	}
	return filepath.Join(Home(), ".newgate.pid")
}

func LegacyLockFile() string {
	if os.Getenv("NEWGATE_HOME") != "" {
		return ""
	}
	return filepath.Join(Home(), ".newgate.lock")
}

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

// EnsureDirs 2770（组可读写 + setgid）：共享部署模型下，同组的其他用户
// （developer 组里的 claude 用户）也要能读配置、写 pid/lock 管理同一个
// daemon。新文件继承组的 setgid 靠目录位，umask 剥掉的组写用 chmod 补回。
// 已存在的目录不碰——权限是装它的人的事。
func EnsureDirs() error {
	for _, d := range []string{Config(), Mappings(), BackupDir()} {
		if _, err := os.Stat(d); err == nil {
			continue
		}
		if err := os.MkdirAll(d, 0o2770); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o2770); err != nil {
			return err
		}
	}
	return nil
}
