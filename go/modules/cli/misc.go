package cli

import (
	"fmt"
	"time"

	"github.com/rzbdz/newgate/go/lib/style"
	"github.com/rzbdz/newgate/go/modules/cli/tui"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// cmdDebug turns on full request logging.
//
// It expires on its own by default: a single request can log 8KB or more
// (opencode's system prompt alone is ~97KB), so leaving it on indefinitely
// fills the disk.
func cmdDebug(args []string) int {
	on := len(args) > 1 && truthy(args[1])
	ttl := 30 * time.Minute
	if on {
		for _, a := range args {
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n > 0 {
				ttl = time.Duration(n) * time.Minute
			}
		}
		if has(args, "--forever") {
			ttl = 0
		}
	}
	if err := store.SetDebug(on, debugUntil(ttl)); err != nil {
		return die(70, err.Error())
	}
	if !on {
		fmt.Println(style.Item(style.Skip, "debug off"))
		return 0
	}
	if ttl > 0 {
		fmt.Println(style.Item(style.OK, fmt.Sprintf("debug on · %s 后自动关闭", prettyDur(int(ttl.Seconds())))))
		fmt.Println(style.Hint("不设期限：newgate debug on --forever"))
	} else {
		fmt.Println(style.Item(style.Warn, "debug on · 不自动关闭"))
		fmt.Println(style.Hint("记得 newgate debug off"))
	}
	fmt.Println(style.Hint("日志 " + paths.LogFile() + " · 16MB 轮转，保留 4 份"))
	notifyProxy()
	if daemon.Running() != nil {
		fmt.Println(style.Hint("即刻生效，无需重启"))
	}
	return 0
}

func debugUntil(ttl time.Duration) string {
	if ttl <= 0 {
		return ""
	}
	return time.Now().Add(ttl).Format(time.RFC3339)
}

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
