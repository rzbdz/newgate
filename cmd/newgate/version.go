package main

// 链接期注入的构建事实，由 Makefile 的 ldflags 写进来（见 Makefile 的 LDFLAGS）。
//
// 它们只在这里声明、只被 entry.MakeProcess 读一次：那一趟把 argv0 归一、版本落进
// buildinfo 叶子，然后所有模块（界面、守护进程）读的都是同一份（见 lib/buildinfo）。
var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)
