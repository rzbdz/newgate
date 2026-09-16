package thinkcache

import (
	"encoding/binary"
	"io"
	"os"
	"sync"
	"time"
)

// diskMagic 文件头：认出这是 thinkcache 的落盘文件。换记录格式时改这个
// 字符串，旧文件会被当作空文件（冷启动，退回纯内存 + 占位符，不报错）。
var diskMagic = []byte("NGTC01\n")

// DiskStore 三级缓存的落盘冷层：append-only 记录文件 + 内存索引（只存
// offset/时间戳，不存 blob 本身），撑过 daemon 重启。
//
// best-effort，不是强一致存储：
//   - 写走 os.File.Write + O_APPEND（进 OS page cache，不 fsync）。进程退出/
//     重启不丢数据（page cache 仍归内核），只有整机掉电才丢最近几条——
//     对「补不回来就用占位符兜底」的缓存够用；
//   - 超过 maxBytes*2 就整体重写一遍（丢过期项），磁盘占用封顶在 ~2×max
//     附近，不会无限增长、不会占满盘（活数据本身超过 max 时无法再缩，只能
//     由 TTL 兜住——8h 内真产不出 100MB 推理）。
type DiskStore struct {
	mu    sync.Mutex
	f     *os.File
	path  string
	size  int64 // 当前文件字节数（近似，判断是否该重写）
	max   int64
	ttl   time.Duration
	index map[string]rec
}

type rec struct {
	off int64 // 这条记录在文件里的起始偏移
	at  time.Time
}

func openDisk(path string, maxBytes int64, ttl time.Duration) (*DiskStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o660)
	if err != nil {
		return nil, err
	}
	d := &DiskStore{f: f, path: path, max: maxBytes, ttl: ttl, index: map[string]rec{}}
	d.scan()
	return d, nil
}

func (d *DiskStore) Close() error { return d.f.Close() }

// scan 扫文件重建索引（调用方须持锁，或启动期单线程）。扫到半条记录
// （进程被杀留下的尾巴）就截掉。空文件/外来文件一律重置成「只有文件头」。
func (d *DiskStore) scan() {
	fi, err := d.f.Stat()
	if err != nil {
		d.size = 0
		return
	}
	d.size = fi.Size()
	// 空文件 / 比头还短 / 不是我们的文件：统一重置，只留文件头
	reset := d.size < int64(len(diskMagic))
	if !reset {
		hdr := make([]byte, len(diskMagic))
		if _, err := d.f.ReadAt(hdr, 0); err != nil || string(hdr) != string(diskMagic) {
			reset = true
		}
	}
	if reset {
		_ = d.f.Truncate(0)
		if _, err := d.f.Write(diskMagic); err == nil {
			d.size = int64(len(diskMagic))
		}
		return
	}
	now := time.Now()
	off := int64(len(diskMagic))
	good := off
	for {
		key, _, at, n, err := readRecAt(d.f, off)
		if err != nil {
			break
		}
		if d.ttl > 0 && now.Sub(at) > d.ttl {
			off += int64(n)
			continue // 过期：不索引，等 compact 一起收
		}
		d.index[key] = rec{off: off, at: at}
		off += int64(n)
		good = off
	}
	if off != d.size {
		_ = d.f.Truncate(good)
		d.size = good
	}
}

// snapshot 拷一份索引（启动期回灌用，避免跨锁读 map）。
func (d *DiskStore) snapshot() map[string]rec {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]rec, len(d.index))
	for k, r := range d.index {
		out[k] = r
	}
	return out
}

// appendKeys 把一段 blob 挂到若干 key 上（镜像 Cache.Put 的多 key 语义）。
func (d *DiskStore) appendKeys(keys []string, blob []byte, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(keys) == 0 || len(blob) == 0 {
		return
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		buf := recBytes(k, blob, at)
		if _, err := d.f.Write(buf); err != nil {
			continue // 写失败就跳过这条，best-effort
		}
		d.index[k] = rec{off: d.size, at: at}
		d.size += int64(len(buf))
	}
	if d.size > d.max*2 {
		d.compactLocked()
	}
}