package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/internal/platform/paths"
)

type Info struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt string `json:"started_at"`
	Exe       string `json:"exe"`
}

func ReadPid() (*Info, error) {
	b, err := ioutil.ReadFile(paths.PidFile())
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
	return ioutil.WriteFile(paths.PidFile(), append(b, '\n'), 0o600)
}

func RemovePid()  { _ = os.Remove(paths.PidFile()) }
func RemoveLock() { _ = os.Remove(paths.LockFile()) }

// Alive 判断进程还在不在。只比 pid 会误判复用，所以���外看 /proc 的 cmdline。
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	b, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return true // 非 Linux 或读不到，退化为只看 kill(0)
	}
	return strings.Contains(string(b), "newgate")
}

// AcquireLock 独占锁。发现死锁文件自动清理。
func AcquireLock() error {
	if b, err := ioutil.ReadFile(paths.LockFile()); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if Alive(pid) {
			return fmt.Errorf("newgate 已在运行 (pid %d)。用 `newgate restart` 或 `newgate stop`", pid)
		}
		// 陈旧锁
		RemoveLock()
		RemovePid()
	}
	f, err := os.OpenFile(paths.LockFile(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("抢锁失败: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	return err
}

// Running 返回当前活着的实例信息。
func Running() *Info {
	i, err := ReadPid()
	if err != nil || i == nil {
		return nil
	}
	if !Alive(i.PID) {
		return nil
	}
	return i
}

// Spawn 把自己以 __serve 模式重新拉起，作为后台守护进程。
func Spawn(port int) (*Info, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command(exe, "__serve", "--port", strconv.Itoa(port))
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 脱离终端
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	info := &Info{PID: cmd.Process.Pid, Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe}
	if err := WritePid(info); err != nil {
		return nil, err
	}
	_ = cmd.Process.Release()
	return info, nil
}

// Stop 优雅停，等不到就强杀。
func Stop() (int, error) {
	i, err := ReadPid()
	if err != nil {
		RemoveLock()
		return 0, nil // 本来就没在跑
	}
	if !Alive(i.PID) {
		RemovePid()
		RemoveLock()
		return 0, nil
	}
	_ = syscall.Kill(i.PID, syscall.SIGTERM)
	for k := 0; k < 40; k++ { // 最多等 2s
		if !Alive(i.PID) {
			RemovePid()
			RemoveLock()
			return i.PID, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(i.PID, syscall.SIGKILL)
	RemovePid()
	RemoveLock()
	return i.PID, nil
}
