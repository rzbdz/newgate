package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate/lib/style"
	"github.com/rzbdz/newgate/modules/gateway/controlplane"
)

// printThinkCache 展示推理内容缓存的命中情况（`newgate st` 末尾那几行）。
//
// **为什么住在这里**（2026-09-18 从 modules/cli 搬来）：推理缓存是数据面的
// 一部分（modules/gateway/thinkcache），「哪些计数值得看、命中和未命中分别意味
// 着什么」全是本模块的知识。它留在界面时，界面得在 Host 上开一个叫
// PrintThinkCache 的口子——那等于界面**按名字认识了一个模块的概念**，正是
// `probe` / `metrics` / `st` 这几个命令搬回来时要拆掉的东西。搬回来之后
// cliapi.Host 不再有一条以某个模块命名的方法。
//
// 数字从**跑着的守护进程**取：缓存在它内存里，命令跑在另一个进程，读本进程的
// thinkcache.Default 只会看到空缓存——那比不显示更误导人。取数走控制面叶子
// （controlplane.State()），与界面无关，任何模块都能读。
//
// 只报计数，永远不报内容。
func printThinkCache() {
	info, ps := controlplane.State()
	if info == nil || ps == nil {
		return // 守护进程没在跑：没有缓存可报
	}
	t := ps.Think
	fmt.Println()
	fmt.Println(style.Field("推理缓存", fmt.Sprintf("%d 条 · %s / %s · 命中 %d · 未命中 %d · 淘汰 %d",
		t.Entries, humanBytes(t.Bytes), humanBytes(t.MaxBytes), t.Hits, t.Misses, t.Evictions)))
	fmt.Println(style.Hint("客户端会丢弃上游的推理内容，代理代为保存并在下一轮原样补回"))
	if t.Misses > 0 {
		fmt.Println(style.Hint("未命中只能补空串（那几轮模型看不到自己的上一轮推理）；日志逐条有记"))
	}
	// 解析不出来的那些和「未命中」在结果上一样（都补空串），但处置完全不同：
	// 前者是客户端发来的 JSON 形态我们不认识，后者是缓存里真没有。混在一起报
	// 会把人引到错的地方，所以单独一行。
	if t.Unparsable > 0 {
		fmt.Println(style.Item(style.Warn, fmt.Sprintf(
			"其中 %d 条是请求里的 assistant 消息解析不出来（不是缓存问题，是客户端发来的形态变了）",
			t.Unparsable)))
	}
	fmt.Println(style.Hint("内存 + thinkcache.bin 冷层；重启后自动装回，只存计数不存内容"))
}

// humanBytes 把字节数写成 KB/MB。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
