package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/store"
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

// 光标/清屏/反显是**终端控制**，留在本地；语义色一律走 ui/style——那边
// 还要处理 NO_COLOR 与「输出不是终端」，两套定义迟早分叉。
const (
	clear = "\033[2J\033[H"
	rev   = "\033[7m"
	reset = "\033[0m"
	hideC = "\033[?25l"
	showC = "\033[?25h"
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