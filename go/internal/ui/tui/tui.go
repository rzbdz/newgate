package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/store"
)

// ---------- 零依赖 raw mode ----------

type termios struct {
	Iflag, Oflag, Cflag, Lflag uint32
	Line                       uint8
	Cc                         [32]uint8
	Ispeed, Ospeed             uint32
}

const (
	tcgets = 0x5401
	tcsets = 0x5402
)

func ioctl(fd uintptr, req uintptr, t *termios) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t)))
	if e != 0 {
		return e
	}
	return nil
}

func enterRaw() (*termios, error) {
	fd := os.Stdin.Fd()
	var old termios
	if err := ioctl(fd, tcgets, &old); err != nil {
		return nil, err
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, tcsets, &raw); err != nil {
		return nil, err
	}
	return &old, nil
}

func restore(old *termios) {
	if old != nil {
		_ = ioctl(os.Stdin.Fd(), tcsets, old)
	}
}

// ---------- 渲染 ----------

const (
	clear  = "\033[2J\033[H"
	bold   = "\033[1m"
	dim    = "\033[2m"
	rev    = "\033[7m"
	reset  = "\033[0m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	hideC  = "\033[?25l"
	showC  = "\033[?25h"
)

type key int

const (
	kUp key = iota
	kDown
	kEnter
	kQuit
	kSave
	kLeft
	kRight
	kOther
)

func readKey(r *bufio.Reader) key {
	b, err := r.ReadByte()
	if err != nil {
		return kQuit
	}
	switch b {
	case '\r', '\n', ' ':
		return kEnter
	case 'q', 'Q', 3, 27: // 27 单独出现也当 ESC；带序列的下面处理
		if b == 27 {
			if r.Buffered() >= 2 {
				b2, _ := r.ReadByte()
				b3, _ := r.ReadByte()
				if b2 == '[' {
					switch b3 {
					case 'A':
						return kUp
					case 'B':
						return kDown
					case 'C':
						return kRight
					case 'D':
						return kLeft
					}
				}
				return kOther
			}
			return kQuit
		}
		return kQuit
	case 'k':
		return kUp
	case 'j':
		return kDown
	case 'h':
		return kLeft
	case 'l':
		return kRight
	case 's', 'S':
		return kSave
	}
	return kOther
}

// ---------- 主界面：选 profile ----------

func Run() error {
	old, err := enterRaw()
	if err != nil {
		return fmt.Errorf("这个终端不支持 TUI（%v）。用 `newgate --set-profile <name>` 代替", err)
	}
	defer restore(old)
	fmt.Print(hideC)
	defer fmt.Print(showC + reset + "\n")

	in := bufio.NewReader(os.Stdin)
	names, err := store.ListProfiles()
	if err != nil || len(names) == 0 {
		return fmt.Errorf("没有 profile，先跑 `newgate init`")
	}
	st := store.LoadState()

	cur := 0
	for i, n := range names {
		if n == st.DefaultProfile {
			cur = i
		}
	}

	msg := ""
	for {
		drawProfileMenu(names, cur, st.DefaultProfile, msg)
		switch readKey(in) {
		case kUp:
			if cur > 0 {
				cur--
			}
			msg = ""
		case kDown:
			if cur < len(names)-1 {
				cur++
			}
			msg = ""
		case kEnter, kRight:
			if err := store.SetActiveProfile("", names[cur]); err != nil {
				msg = yellow + "✗ " + err.Error() + reset
			} else {
				st = store.LoadState()
				msg = green + "✓ 已切到 " + names[cur] + "（运行中的会话下个请求即生效）" + reset
			}
		case kQuit:
			return nil
		}
	}
}

func drawProfileMenu(names []string, cur int, active, msg string) {
	var b strings.Builder
	b.WriteString(clear)
	b.WriteString(bold + " newgate · profile 选择" + reset + "\n")
	b.WriteString(dim + " ↑/↓ 或 j/k 移动 · Enter 应用 · q 退出" + reset + "\n\n")

	provs, _ := store.LoadProviders()
	for i, n := range names {
		mark := " "
		if n == active {
			mark = green + "*" + reset
		}
		line := fmt.Sprintf(" %s %-10s", mark, n)
		if i == cur {
			line = rev + line + reset
		}
		b.WriteString(line)

		if pr, err := store.LoadProfile(n); err == nil {
			b.WriteString("  " + dim + pr.Description + reset)
		}
		b.WriteString("\n")

		if i == cur {
			if pr, err := store.LoadProfile(n); err == nil {
				for _, role := range domain.Roles {
					bind, ok := pr.Resolve(role)
					if !ok {
						b.WriteString(fmt.Sprintf("        %-8s %s未绑定%s\n", role, yellow, reset))
						continue
					}
					warn := ""
					if provs != nil {
						if p, ok2 := provs.Providers[bind.Provider]; !ok2 {
							warn = yellow + "  ← provider 未定义" + reset
						} else if p.Key() == "" {
							warn = yellow + "  ← 缺 api_key" + reset
						}
					}
					b.WriteString(fmt.Sprintf("        %-8s %s%s/%s%s%s\n",
						role, cyan, bind.Provider, bind.Model, reset, warn))
				}
			}
		}
	}
	if msg != "" {
		b.WriteString("\n " + msg + "\n")
	}
	fmt.Print(b.String())
}
