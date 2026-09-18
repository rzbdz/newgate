package cli

import (
	"fmt"

	"github.com/rzbdz/newgate/go/lib/style"
	"github.com/rzbdz/newgate/go/modules/cli/tui"
)

func cmdTUI() int {
	if err := tui.Run(); err != nil {
		return die(70, err.Error())
	}
	return 0
}

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
