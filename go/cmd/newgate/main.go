// Command newgate 是进程的**唯一组合根**：装图、把这次调用交给入口、退出。
//
// 它不认识任何一个模块。这次调用归谁（控制 CLI、某个被接管的 client、将来的
// web-daemon）是**模块自己的申报**，写在 root 的入口账本里（见 component/entry
// 与 go/root）。所以本文件里没有 cli、没有 wrapper，也没有任何模块的名字——
// 换掉界面、加一个新的前端，这里一行都不用改。
//
// 它做的三件事，按顺序：
//
//  1. 装配留痕（app.TraceToLogFile）——谁装了什么、什么顺序，每个进程都记
//  2. 起图（app.New）——失败就是 70，绝不带着半张图往下走
//  3. 问入口（entry）——没人认领时说一句人话退出，不是崩
package main

import (
	"context"
	"fmt"
	"os"

	app "github.com/rzbdz/newgate/go/app"
	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/component/entry"
)

func main() {
	os.Exit(run())
}

func run() int {
	// 装配过程留痕：谁装了什么、什么顺序、谁的弱依赖没命中。**每个进程都记**——
	// 优雅交接那次最需要它（新进程在后台装配，用户全程看不见），而那时判不出
	// 「我是守护进程」（见 app.TraceToLogFile 的注释）。收尾函数在退出前摘掉出口。
	stopTrace := app.TraceToLogFile()
	defer stopTrace()

	built, err := app.New(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "newgate:", err)
		return 70
	}
	defer built.Stop(context.Background())

	// 这次进程调用的事实：argv0 归一、版本注入都在 MakeProcess 里一次做完。
	p := entry.MakeProcess(os.Args, os.Environ(), version, buildTime, commitTime)

	// 记第一行日志：**这次调用 route 给谁**。它是排查「我敲的明明是 newgate，
	// 为什么起的是别的壳」的唯一现场——判据是模块申报的，不是这里猜的。
	registry := modules.MustGet(built.Context(), entry.Capability)
	handler, why, ok := registry.Resolve(p)
	if !ok {
		fmt.Fprintln(os.Stderr, "newgate: "+why)
		return 69
	}
	modules.Tracef("入口解析：argv0=%q → %s（%s）", p.Argv0, handler.Name(), why)
	return handler.Handle(p)
}
