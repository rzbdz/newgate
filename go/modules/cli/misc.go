package cli

import (
	"fmt"

	"github.com/rzbdz/newgate/go/lib/style"
)

// printThinkCache 展示推理内容缓存的命中情况。
//
// 数字必须从**跑着的守护进程**取：缓存在守护进程的内存里，CLI 是另一个进程，
// 在这边读 thinkcache.Default 只会看到一个空缓存——那比不显示更误导人。
//
// 只报计数，永远不报内容。
func printThinkCache() {
	info, ps := proxyState()
	if info == nil || ps == nil {
		return
	}
	t := ps.Think
	fmt.Println()
	fmt.Println(style.Field("推理缓存", fmt.Sprintf("%d 条 · %s / %s · 命中 %d · 未命中 %d · 淘汰 %d",
		t.Entries, human(t.Bytes), human(t.MaxBytes), t.Hits, t.Misses, t.Evictions)))
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

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
