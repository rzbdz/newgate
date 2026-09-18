package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Rotator 一个带大小上限的日志写入器。写满就轮转，只保留 keep 份历史。
//
// 必须有：debug 模式下单条请求可能记 8KB+（opencode 的系统提示就有 97KB），
// 没有上限的话磁盘很快就满。
type Rotator struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

func New(path string, maxBytes int64, keep int) (*Rotator, error) {
	r := &Rotator{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotator) open() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			// 轮转失败也别丢日志，继续往当前文件写
			fmt.Fprintf(os.Stderr, "newgate: 日志轮转失败: %v\n", err)
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate 把 x.log → x.log.1 → x.log.2 …，超出 keep 的删掉。
//
// 失败必须报出来：轮转不发生时没有任何现象，只是日志文件一直长——而日志是
// 这个项目排查问题的唯一手段（CLAUDE.md §5），一个悄悄涨到几 GB 的日志既
// 拖慢 grep 也可能把盘写满。所以这里把每一步的错误收起来返回，由 Write 打
// 到 stderr（原来每个 `_ =` 后面都是空的）。
//
// 「源文件不存在」不算错：还没轮到那么多代，或者上一轮已经删过——那是
// 正常状态，报出来只会刷屏。
func (r *Rotator) rotate() error {
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
	// 从旧到新挪，避免覆盖
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		from, to := fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("轮转 %s → %s: %w", from, to, err)
		}
	}
	// 这一条是真正要紧的：它失败 = 当前日志没被挪走 = 之后继续往同一个文件写，
	// 于是 maxBytes 形同虚设。
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("轮转 %s → %s.1: %w", r.path, r.path, err)
	}
	return r.open()
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// PruneDir 只保留目录下按名字排序最新的 keep 个文件（按前缀分组计数）。
// 用于限制错误证据 dump 的数量。
func PruneDir(dir string, keep int) { PruneDirBy(dir, "", keep) }

// PruneDirBy 只清理以 prefix 开头的文件组（空前缀 = 全部）。dump 的
// req-* 和错误证据的 err-* 共用一个目录、各自设上限，互不删对方。
func PruneDirBy(dir, prefix string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// 按 "err-400-req000003" 这样的前缀分组，一组是四个文件
	groups := map[string]bool{}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if prefix != "" && !strings.HasPrefix(n, prefix) {
			continue
		}
		base := n
		if i := indexAny(n, "."); i > 0 {
			base = n[:i]
		}
		if !groups[base] {
			groups[base] = true
			names = append(names, base)
		}
	}
	if len(names) <= keep {
		return
	}
	sortStrings(names)
	for _, base := range names[:len(names)-keep] {
		for _, e := range ents {
			if len(e.Name()) >= len(base) && e.Name()[:len(base)] == base {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
