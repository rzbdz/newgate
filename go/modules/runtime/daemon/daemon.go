package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/lib/httpx"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/store"
)

type Info struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt string `json:"started_at"`
	Exe       string `json:"exe"`
}

func ReadPid() (*Info, error) {
	b, err := ioutil.ReadFile(paths.PidFile())
	if err != nil && paths.LegacyPidFile() != "" {
		// 老版本把 pid 写在 $HOME——升级后第一次 stop/status 靠它找到
		// 还在跑的旧 daemon（找到后会顺手清掉）。
		b, err = ioutil.ReadFile(paths.LegacyPidFile())
	}
	if err != nil {
		return nil, err
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		// 兼容纯数字 pid 文件
		if n, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil {
			return &Info{PID: n}, nil
		}
		return nil, err
	}
	return &i, nil
}

func WritePid(i *Info) error {
	b, _ := json.MarshalIndent(i, "", "  ")
	return ioutil.WriteFile(paths.PidFile(), append(b, '\n'), 0o660)
}

// RemovePid / RemoveLock 两处（共享目录 + 老的 $HOME 位置）都清：
// stop 一个 legacy pid 指到的旧 daemon 时，老文件得一起带走，否则
// 下次又把它当活实例读出来。
func RemovePid() {
	_ = os.Remove(paths.PidFile())
	if p := paths.LegacyPidFile(); p != "" {
		_ = os.Remove(p)
	}
}

func RemoveLock() {
	_ = os.Remove(paths.LockFile())
	if p := paths.LegacyLockFile(); p != "" {
		_ = os.Remove(p)
	}
}

// Alive 判断进程还在不在。只比 pid 会误判复用，所以还额外看 /proc 的 cmdline。
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		// EPERM：进程**在**，只是不归我们管（共享部署里别的用户起的
		// daemon）。「活没活着」和「能不能发信号」是两回事——后者由
		// Stop 走控制端点兜底。把它误判成不在，claude 用户就永远看到
		// 「代理没在跑」，这正是要修的「看不到状态」。
		if err != syscall.EPERM {
			return false
		}
	}
	b, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return true // 非 Linux 或读不到，退化为只看 kill(0)
	}
	// 空 cmdline 有两种含义，必须区分：
	//   1. 正在 exec 的过渡态（换镜像的瞬间 /proc 短暂读出空串）——活着。
	//      Spawn 刚返回就 Running()/Stop() 最容易撞上这个窗口，误判成
	//      死会引发「明明刚拉起却报没在跑」；
	//   2. 僵尸（死了没人收尸）——死。
	if len(b) == 0 {
		return !isZombie(pid)
	}
	return strings.Contains(string(b), "newgate")
}

// isZombie 看 /proc/<pid>/stat 的状态位。stat 的 comm 字段可含空格和括号，
// 所以从最后一个 ')' 之后取状态字符。
func isZombie(pid int) bool {
	st, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	if i := bytes.LastIndexByte(st, ')'); i >= 0 && i+2 < len(st) {
		return st[i+2] == 'Z'
	}
	return false
}

// AcquireLock 独占锁。发现死锁文件自动清理。
func AcquireLock() error {
	// 新老两个位置都查：老 daemon 的活锁在 $HOME，不认它就会起出
	// 第二个实例去撞端口。
	for _, f := range []string{paths.LockFile(), paths.LegacyLockFile()} {
		if f == "" {
			continue
		}
		if b, err := ioutil.ReadFile(f); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if Alive(pid) {
				return fmt.Errorf("newgate 已在运行 (pid %d)。用 `newgate restart` 或 `newgate stop`", pid)
			}
			// 陈旧锁
			RemoveLock()
			RemovePid()
		}
	}
	f, err := os.OpenFile(paths.LockFile(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o660)
	if err != nil {
		return fmt.Errorf("抢锁失败: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	return err
}