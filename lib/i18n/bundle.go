package i18n

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// 目录表的**编译期形态**：解析这件事在生成期做完，运行期只按格式读。
//
// # 为什么值得一个格式
//
// 2026-09-21 实测：每条 `newgate …` 命令的装配约 6ms，其中 **88% 是每次进程都
// 重新解析那 250KB 目录 JSON**（`Builtin()` 单独量是 2.78ms，而 `Install` 建查找表
// 只要 90µs）。真要挪到编译期的是**这个**，不是 resolve——`component.Resolve` 的
// 校验加拓扑排序一共 31µs（0.5%）。
//
// 所以：`tools/i18n bundle` 在生成期把 catalogs/*.json 编成这个格式（进版本控制，
// `-check` 拦过期，同 manifest/modules_gen.go 那条路），运行期 `Builtin()` 直接读
// 它。**JSON 仍然是唯一的真相**——人改的是 JSON，工具读写的是 JSON，这个格式是
// 纯派生物，随时可以删了重生成。
//
// # 为什么不是「读到 map 里就完了」
//
// 格式解码出来仍然是 `Catalog`/`Ledger`（map 在里面），**公开 API 一个字没动**：
// 网关日志热路径上的 `T()` 还是哈希查找。换掉的只是「从字节到 map」这一步——
// 从「JSON 词法分析 + 2800 次反射反序列化」变成「读长度前缀 + 插 map」。
//
// # 格式
//
//	魔数 "NGCAT1\n"（8 字节）
//	类型 1 字节           'c' 译文 / 'l' 账本
//	u32 段数 → 每段      u32 长度 + 字节（字符串一律这样放；u32 是为了不用想上限）
//	条目按**键排序**写：同一份 JSON 生成的字节必须逐字节相同，否则 -check 会
//	因为 map 遍历序不同而永远报「过期」，而那种假警报会让人把这条检查关掉。
//
// 读的每一个长度都做边界检查：这个格式是**生成物**，但它可能来自一次失败的构建
// （半截文件）或者一个被别人换过的仓库。越界读是崩溃，比报一句错严重得多。
const bundleMagic = "NGCAT1\n"

const (
	bundleKindCatalog = 'c'
	bundleKindLedger  = 'l'
)

// ---------- 写 ----------

type bundleWriter struct{ b []byte }

func (w *bundleWriter) u32(v uint32) {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	w.b = append(w.b, tmp[:]...)
}

func (w *bundleWriter) u8(v byte) { w.b = append(w.b, v) }

func (w *bundleWriter) str(s string) {
	w.u32(uint32(len(s)))
	w.b = append(w.b, s...)
}

// EncodeCatalog 把一份译文编成 bundle 字节。
func EncodeCatalog(c Catalog) []byte {
	w := &bundleWriter{b: append([]byte(nil), bundleMagic...)}
	w.u8(bundleKindCatalog)
	w.str(c.Language)
	w.str(c.Source)

	widthKeys := make([]string, 0, len(c.Widths))
	for k := range c.Widths {
		widthKeys = append(widthKeys, k)
	}
	sort.Strings(widthKeys)
	w.u32(uint32(len(widthKeys)))
	for _, k := range widthKeys {
		w.str(k)
		w.u32(uint32(c.Widths[k]))
	}

	ids := make([]string, 0, len(c.Messages))
	for id := range c.Messages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w.u32(uint32(len(ids)))
	for _, id := range ids {
		e := c.Messages[id]
		w.str(id)
		w.str(e.Text)
		w.str(e.One)
		w.str(e.Other)
		w.str(e.Note)
		var flags byte
		if e.Machine {
			flags |= 1
		}
		if e.Reviewed {
			flags |= 2
		}
		w.u8(flags)
	}
	return w.b
}

// EncodeLedger 把账本编成 bundle 字节。
func EncodeLedger(l Ledger) []byte {
	w := &bundleWriter{b: append([]byte(nil), bundleMagic...)}
	w.u8(bundleKindLedger)

	ids := make([]string, 0, len(l.Messages))
	for id := range l.Messages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w.u32(uint32(len(ids)))
	for _, id := range ids {
		e := l.Messages[id]
		w.str(id)
		w.str(e.Where)
		args := append([]string(nil), e.Args...)
		sort.Strings(args)
		w.u32(uint32(len(args)))
		for _, a := range args {
			w.str(a)
		}
		w.str(e.One)
		w.str(e.Other)
		w.str(e.Note)
	}
	return w.b
}

// ---------- 读 ----------

type bundleReader struct {
	b   []byte
	at  int
	err error
}

// hint 把一个**来自文件**的条数变成可以安全交给 make 的容量提示。
//
// 为什么需要：`make(map[K]V, n)` 会按 n 预分配，而 n 是文件里写着的数。一个坏掉的
// 或者被人换过的 bundle 可以写着 40 亿条——那会在**第一处边界检查生效之前**先要一次
// 巨额分配，症状是启动时 OOM（或者干脆 panic），而不是一句「这个 bundle 是坏的」。
// 这里按「剩下多少字节」夹一次：每条至少 perEntry 个字节，超出这个数的条数一定是
// 假的。夹的是**容量提示**，不是判据——真正的越界仍然由每一步的检查拦。
func (r *bundleReader) hint(n uint32, perEntry int) int {
	if r.err != nil {
		return 0
	}
	most := uint32((len(r.b) - r.at) / perEntry)
	if n > most {
		return int(most)
	}
	return int(n)
}

// fail 记下第一处越界就停：后面每一步都拿 err 短路，不必每处都写 if。
func (r *bundleReader) fail(what string) {
	if r.err == nil {
		r.err = fmt.Errorf("the message bundle is truncated or corrupt (%s at byte %d)", what, r.at)
	}
}

func (r *bundleReader) u32() uint32 {
	if r.err != nil {
		return 0
	}
	if r.at+4 > len(r.b) {
		r.fail("length")
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.at:])
	r.at += 4
	return v
}

func (r *bundleReader) u8() byte {
	if r.err != nil {
		return 0
	}
	if r.at+1 > len(r.b) {
		r.fail("flag")
		return 0
	}
	v := r.b[r.at]
	r.at++
	return v
}

func (r *bundleReader) str() string {
	if r.err != nil {
		return ""
	}
	n := r.u32()
	if r.err != nil {
		return ""
	}
	// 长度是**文件里写着的数**：它可能是垃圾。按剩余字节夹一次再切，否则一次
	// 越界读就是 panic，而这里一个 panic 会让整个进程起不来（目录表在 Start 里读）。
	if int(n) > len(r.b)-r.at {
		r.fail("string length")
		return ""
	}
	s := string(r.b[r.at : r.at+int(n)])
	r.at += int(n)
	return s
}

// DecodeCatalog 读一份 bundle 译文。
func DecodeCatalog(raw []byte) (Catalog, error) {
	kind, r, err := openBundle(raw)
	if err != nil {
		return Catalog{}, err
	}
	if kind != bundleKindCatalog {
		return Catalog{}, fmt.Errorf("expected a translation bundle, found kind %q", kind)
	}
	out := Catalog{Language: r.str(), Source: r.str()}
	if n := r.u32(); n > 0 {
		out.Widths = make(map[string]int, r.hint(n, 8))
		for i := uint32(0); i < n && r.err == nil; i++ {
			k := r.str()
			out.Widths[k] = int(int32(r.u32()))
		}
	}
	n := r.u32()
	out.Messages = make(map[string]Entry, r.hint(n, 5))
	for i := uint32(0); i < n && r.err == nil; i++ {
		id := r.str()
		e := Entry{Text: r.str(), One: r.str(), Other: r.str(), Note: r.str()}
		flags := r.u8()
		e.Machine = flags&1 != 0
		e.Reviewed = flags&2 != 0
		out.Messages[id] = e
	}
	if r.err != nil {
		return Catalog{}, r.err
	}
	if out.Language == "" {
		return Catalog{}, fmt.Errorf("the translation bundle has no language")
	}
	return out, nil
}

// DecodeLedger 读一份 bundle 账本。
func DecodeLedger(raw []byte) (Ledger, error) {
	kind, r, err := openBundle(raw)
	if err != nil {
		return Ledger{}, err
	}
	if kind != bundleKindLedger {
		return Ledger{}, fmt.Errorf("expected a ledger bundle, found kind %q", kind)
	}
	n := r.u32()
	out := Ledger{Messages: make(map[string]LedgerEntry, r.hint(n, 5))}
	for i := uint32(0); i < n && r.err == nil; i++ {
		id := r.str()
		e := LedgerEntry{Where: r.str()}
		if na := r.u32(); na > 0 {
			e.Args = make([]string, 0, r.hint(na, 4))
			for j := uint32(0); j < na && r.err == nil; j++ {
				e.Args = append(e.Args, r.str())
			}
		}
		e.One, e.Other, e.Note = r.str(), r.str(), r.str()
		out.Messages[id] = e
	}
	if r.err != nil {
		return Ledger{}, r.err
	}
	return out, nil
}

func openBundle(raw []byte) (byte, *bundleReader, error) {
	if len(raw) < len(bundleMagic)+1 || string(raw[:len(bundleMagic)]) != bundleMagic {
		return 0, nil, fmt.Errorf("not a message bundle (bad magic)")
	}
	r := &bundleReader{b: raw, at: len(bundleMagic)}
	kind := r.u8()
	if r.err != nil {
		return 0, nil, r.err
	}
	return kind, r, nil
}
