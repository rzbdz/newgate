package app

import (
	"context"
	"fmt"
	"os"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
)

// Options 是进程组合根的输入：装哪张图 + 版本三件套。
//
// Loader 为 nil 表示内核自带的那张图（Loader{}）——内核自己的二进制、
// 以及只想知道「内核默认装什么」的调用方走这条。
type Options struct {
	Loader     modules.Loader
	Version    string
	BuildTime  string
	CommitTime string
}

// Main 是组合根的全部逻辑，按顺序做三件事：
//
//  1. 装配留痕（TraceToLogFile）——谁装了什么、什么顺序，每个进程都记；
//  2. 起图——失败就是 70，绝不带着半张图往下走；
//  3. 问入口——没人认领时说一句人话退出（69），不是崩。
//
// 为什么这段逻辑住在内核而不是各家的 main 里：**它不认识任何模块**，所以它对
// 每个消费者都一模一样。抄一份到发行版的 main 里，两份就会开始漂移——而漂移的
// 症状是「发行版里留痕没了 / 退出码不一样」，属于最难注意到的那类差异。
// 各家 main 因此只剩「传哪张图 + 版本号」这一件事。
//
// 优雅交接那次最需要留痕（新进程在后台装配，用户全程看不见），而那时判不出
// 「我是守护进程」（见 TraceToLogFile 的注释），所以它在这里无条件记。
func Main(ctx context.Context, opts Options) int {
	stopTrace := TraceToLogFile()
	defer stopTrace()

	built, err := New(ctx, opts.Loader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "newgate:", err)
		return 70
	}
	defer built.Stop(context.Background())

	// 这次进程调用的事实：argv0 归一、版本注入都在 MakeProcess 里一次做完。
	p := entry.MakeProcess(os.Args, os.Environ(), opts.Version, opts.BuildTime, opts.CommitTime)

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
