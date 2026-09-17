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

func Config() string         { return root() }
func Mappings() string       { return filepath.Join(root(), "mappings") }
func ProvidersFile() string  { return filepath.Join(root(), "providers.json") }
func StateFile() string      { return filepath.Join(root(), "state.json") }
func HealthFile() string     { return filepath.Join(root(), "health.json") }
func ProbeCacheFile() string { return filepath.Join(root(), "probe-capabilities.json") }
func BackupDir() string      { return filepath.Join(root(), "backups") }
func LogFile() string        { return filepath.Join(root(), "newgate.log") }

// ThinkCacheFile thinkcache 的落盘冷层文件：滚动 100MB，撑过 daemon 重启。
// 内容是推理原文（= 对话内容），和 dump 一样是明文，权限 0660 随目录的
// 组共享模型走（developer 组内可见）。
func ThinkCacheFile() string { return filepath.Join(root(), "thinkcache.bin") }

// SecretsFile 是配置共享的密钥落盘处：`{"version":1,"keys":{"<provider>":"sk-…"}}`。
//
// 为什么不放 state.json：state.json 被 watcher 的签名盯着（watch.go 的
// signature()），每次写都会触发一次全量 reload 加一行日志。密钥是**宿主推
// 过来的**，跟着轮询周期改，写在这里等于给配置系统装了个节拍器。
//
// 0600 而不是 providers.json 的 0660：providers 里只有绑定关系（组内可见是
// 有意的），明文 key 不是——谁能读它谁就能直接花掉那份订阅。
// 唯一的例外是共享部署里 daemon 以另一个用户跑（见 CLAUDE.md §3.1 的权限坑），
// 那时按需要 chmod 0660 并保证组边界就是信任边界。
func SecretsFile() string { return filepath.Join(root(), "secrets.json") }

// ConfigShareDir 是配置共享模块自己的记账目录。
//
// 与配置目录分开，是因为这里的东西**都不该被 watcher 看见**：轮询的失败计数
// 每 30 秒就可能变一次，放进去就是一个恒定的热更新源。同理它也不在
// EnsureDirs 里——没用这个功能的机器不该多出目录，用到时才建。
func ConfigShareDir() string { return filepath.Join(root(), "configshare") }

// ConfigShareStateFile 副本侧记账：已应用的 generation、我们写下去的每个文件
// 的 sha256（漂移守卫的基准）、失败计数。
func ConfigShareStateFile() string { return filepath.Join(ConfigShareDir(), "state.json") }

// ConfigShareHostFile 宿主侧记账：权威身份 + 单调 generation。
//
// host_id 与 generation 在**同一个文件**里，因为「换了权威」和「代数重置」必须
// 一起生效：分成两个文件就会有一段两个 rename 之间的撕裂状态，而副本正是靠
// 这两个值共同判断「这份快照该不该应用」。
func ConfigShareHostFile() string { return filepath.Join(ConfigShareDir(), "host.json") }

// ConfigShareLockFile 后台轮询的建议锁。优雅交接时有几百毫秒新旧 daemon 同时
// 在，两个 poller 同时写同一批文件——用 flock 让后到的那个安静跳过这一轮
// （拿不到锁不是错误，见 configshare/proto/poller.go）。
func ConfigShareLockFile() string { return filepath.Join(ConfigShareDir(), "lock") }

// ConfigShareRootKeyFile 预共享根密钥（`newgate config trust` 写进来的那 32 字节）。
// 0600：拿到它就等于拿到 AES-GCM 密钥和 bearer token 两个派生值。
func ConfigShareRootKeyFile() string { return filepath.Join(ConfigShareDir(), "root.key") }

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
