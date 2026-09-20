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

// Root 是整套运行时文件的根目录：`$NEWGATE_HOME` > `$XDG_CONFIG_HOME/newgate`
// > `~/.config/newgate`。pid / lock / 日志 / 配置全在它下面。
//
// 它对外公开（2026-09-20 起）是给**发行版**用的：产品要在这里放自己的东西时，
// 该由它自己拼子路径（`filepath.Join(paths.Root(), "configshare")`），而不是让
// 内核替它把每个文件名都写一遍——那些名字是产品的知识，写在内核里就是内核在
// 认识产品。内核自己用到的名字仍然在下面这一串函数里。
func Root() string {
	if v := os.Getenv("NEWGATE_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "newgate")
	}
	return filepath.Join(Home(), ".config", "newgate")
}

func Config() string         { return Root() }
func Mappings() string       { return filepath.Join(Root(), "mappings") }
func ProvidersFile() string  { return filepath.Join(Root(), "providers.json") }
func StateFile() string      { return filepath.Join(Root(), "state.json") }
func HealthFile() string     { return filepath.Join(Root(), "health.json") }
func ProbeCacheFile() string { return filepath.Join(Root(), "probe-capabilities.json") }
func BackupDir() string      { return filepath.Join(Root(), "backups") }
func LogFile() string        { return filepath.Join(Root(), "newgate.log") }

// ThinkCacheFile thinkcache 的落盘冷层文件：滚动 100MB，撑过 daemon 重启。
// 内容是推理原文（= 对话内容），和 dump 一样是明文，权限 0660 随目录的
// 组共享模型走（developer 组内可见）。
func ThinkCacheFile() string { return filepath.Join(Root(), "thinkcache.bin") }

// pid / lock 放共享配置目录，便于同一组的多用户管理同一个 daemon。
// NEWGATE_HOME 仍可用于测试时隔离整套运行时文件。
func runtimeDir() string {
	return Root()
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
