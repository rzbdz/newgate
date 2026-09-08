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

	"github.com/rzbdz/newgate/go/internal/platform/httpx"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/store"
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
	// 0660：共享部署下别的用户也能读日志排查（目录 setgid 保证组一致）
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
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

// SpawnHandoff 是 nginx 式优雅升级的核心动作：把监听 socket 作为 fd 交给
// 新二进制，等它接管成功（ready 字节）后返回新进程信息。
//
// 为什么这能零停机：socket 的可用性由内核维护，和进程生死解耦。父进程
// 把 fd dup 给子进程，两个进程短暂同时在同一个 socket 上 accept（内核
// 分流），随后父进程只负责把在途请求流完——客户端自始至终没有任何一毫秒
// 面对过「连接被拒」。开发 newgate 的 Claude Code 会话本身就穿行在代理
// 里，restart 杀掉自己脚下这条线的事不能再发生。
//
// 约定（Serve 端配合）：
//   - fd3 = 监听 socket；fd4 = ready 管道写端
//   - 子进程绑好 socket、即将进入 accept 循环时往 fd4 写一个字节
func SpawnHandoff(port int, ln net.Listener) (*Info, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("交接需要 TCP 监听器，实际 %T", ln)
	}
	// File() 返回 dup 出来的新 fd——父进程自己的 listener 不受影响，
	// 之后的 Shutdown 关的是父进程那份。
	listenerFile, err := tcp.File()
	if err != nil {
		return nil, fmt.Errorf("提取监听 fd 失败: %w", err)
	}
	defer listenerFile.Close()

	// ready 管道：子进程接上 socket 的唯一凭证。等不到它就交棒失败。
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readyR.Close()

	// 0660：共享部署下别的用户也能读日志排查（目录 setgid 保证组一致）
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command(exe, "__serve", "--port", strconv.Itoa(port))
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 脱离终端
	cmd.ExtraFiles = []*os.File{listenerFile, readyW}    // fd3=socket, fd4=ready
	cmd.Env = append(os.Environ(),
		"NEWGATE_LISTENER_FD=3", "NEWGATE_READY_FD=4")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// 父进程立刻放下写端：否则子进程就算死了，读端也等不来 EOF。
	readyW.Close()
	if err := readyR.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	var b [1]byte
	if _, err := io.ReadFull(readyR, b[:]); err != nil {
		// 新进程没接上：杀掉它，本进程继续独占 socket，服务未受任何影响
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("新进程 5 秒内没接上监听 socket: %v", err)
	}
	info := &Info{PID: cmd.Process.Pid, Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe}
	_ = cmd.Process.Release()
	return info, nil
}

// AdoptRuntime 把 pid 文件和锁的归属改到新进程——优雅交接后由它持有。
// 只动共享目录：legacy 的 $HOME 位置在交接时早就迁移完了。
func AdoptRuntime(i *Info) error {
	if err := WritePid(i); err != nil {
		return err
	}
	return ioutil.WriteFile(paths.LockFile(),
		[]byte(strconv.Itoa(i.PID)+"\n"), 0o660)
}

// Stop 优雅停，等不到就强杀。
//
// 共享部署的兜底：daemon 可能是别的用户起的（root 起的、claude 用户来停）。
// 同一个组只给读文件的权限，不给 kill() 的权限——信号发不出去（EPERM）时
// 走代理自己的控制端点（POST /__newgate/stop + ControlToken）让它自己退。
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
	if err := syscall.Kill(i.PID, syscall.SIGTERM); err == syscall.EPERM {
		return stopViaHTTP(i)
	}
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

// stopViaHTTP 让别的用户起的 daemon 自己退出。
//
// 令牌在 state.json 里，和 providers 的 key 同级保密（0660、组可读）——
// 能读到它的组员本来就是这台机器的受信用户，停机权限给得合理。
func stopViaHTTP(i *Info) (int, error) {
	st := store.LoadState()
	if st.ControlToken == "" {
		return 0, fmt.Errorf("代理 (pid %d) 由其他用户运行且没有控制令牌——"+
			"请让启动它的用户执行一次 `newgate stop`（之后任意组员都能停）", i.PID)
	}
	port := i.Port
	if port <= 0 {
		port = st.Port
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/__newgate/stop", port), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+st.ControlToken)
	resp, err := httpx.LocalClient(3 * time.Second).Do(req)
	if err != nil {
		return 0, fmt.Errorf("向代理 (pid %d) 发停机请求失败: %w", i.PID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("代理拒绝了停机请求（HTTP %d）", resp.StatusCode)
	}
	for k := 0; k < 40; k++ { // 最多等 2s
		if !Alive(i.PID) {
			RemovePid()
			RemoveLock()
			return i.PID, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("停机请求已被接受，但代理 (pid %d) 2 秒内没有退出（看日志 %s）",
		i.PID, paths.LogFile())
}
