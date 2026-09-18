// 本文件把**组件图的装配过程**接到日志上。
//
// 内核只提供事实出口（component.SetTrace），不认识日志、不认识文件、不认识
// 环境变量；「报不报、报去哪、什么时候报」是产品层的决定，都在这一处。
package app

import (
	"io"
	"log"
	"os"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/paths"
)

// TraceTo 把装配过程（扫描 → 声明 → 构图 → 启动 → 停止）写给这些 writer，
// 返回收尾函数（进程退出前调用）。不传 writer 等于关掉。
//
// 它是这一族里唯一的原语，别处（含子进程）都从它出发——所以它不依赖任何
// 具体目的地，也不需要是 *log.Logger：装配过程一律带时间戳输出，而
// log.Logger 本身就是个 io.Writer。
func TraceTo(writers ...io.Writer) func() {
	live := make([]io.Writer, 0, len(writers))
	for _, w := range writers {
		if w != nil {
			live = append(live, w)
		}
	}
	if len(live) == 0 {
		modules.SetTrace(nil)
		return func() {}
	}
	lg := make([]*log.Logger, 0, len(live))
	for _, w := range live {
		lg = append(lg, log.New(w, "", log.LstdFlags))
	}
	modules.SetTrace(func(format string, args ...any) {
		for _, l := range lg {
			l.Printf(format, args...)
		}
	})
	return func() { modules.SetTrace(nil) }
}

// TraceToLogFile 把装配过程追加进 newgate 的日志文件；`NEWGATE_TRACE=1` 时**同时**
// 打到 stderr，方便本地跑一发亲眼看清顺序（那一份不该是默认行为——CLI 的输出是
// 给人读的，`newgate tier normal` 的答案不该被几十行装配日志埋掉）。
//
// # 为什么每个进程都记，而不是只记守护进程
//
// 装配过程是「这一版到底起了哪几个模块、什么顺序、谁的弱依赖没命中」的唯一一手
// 材料——它只在装配期存在，事后从 Manager 里问不出来（那里只有结果）。
//
// 最需要它的场合是**优雅交接**：旧进程还在服务，新进程在后台装配，用户全程看不
// 见它。而 `newgate restart` 的优雅路径是**旧进程**把 socket 交给新进程的，旧二
// 进制不可能给新二进制带上「你是守护进程」这类的标记——判据必须是进程**自己**
// 知道的。用「argv 里有 __serve」当判据等于把 gateway 的动词名抄进 main，用环境
// 变量当判据则恰好在最需要它的那一次升级上失效。所以这里不做区分：**谁装配了
// 图，谁就留下这一段**。
//
// 代价也说清楚：每次 CLI 调用都会往日志里写一段（本机实测整图 17 个组件约 40 行）。
// 换来的是「升级后 `newgate logs` 里能逐行对照新旧两版的装配」。嫌吵用
// `newgate logs` 的过滤看代理那几行即可。
//
// # 打不开就静默跳过
//
// 典型现场：root 的 CLI 撞上 claude 建的 `0640 root:developer`（CLAUDE.md §3.1）。
// 这里不报也不拦，是有意的取舍——留痕是诊断素材，不该让每条 `newgate status` 都
// 往终端吐一行权限警告，而那个权限问题本身已经在别的路径上响亮地报过
// （thinkcache 落盘关闭、控制令牌写不出去）。
func TraceToLogFile() func() {
	f, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		if os.Getenv("NEWGATE_TRACE") != "" {
			return TraceTo(os.Stderr)
		}
		return TraceTo()
	}
	stop := TraceTo(f, stderrWhenAsked())
	return func() {
		stop()
		_ = f.Close()
	}
}

// stderrWhenAsked：NEWGATE_TRACE 设了才交 stderr 出去，否则返回 nil（TraceTo 会跳过）。
func stderrWhenAsked() io.Writer {
	if os.Getenv("NEWGATE_TRACE") != "" {
		return os.Stderr
	}
	return nil
}
