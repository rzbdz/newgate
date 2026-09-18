// Command newgate is the entry point for both the control CLI and, via a
// PATH shim, the wrapper that injects environment for an agent.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	app "github.com/rzbdz/newgate/go/app"
	cliapi "github.com/rzbdz/newgate/go/modules/cli"
)

// main 只做组合：起组件图，然后把这次调用交给两条通路之一。
//
//	argv0 是某个 client 名（PATH shim 转过来的）→ wrapper 模块注入 env 并 exec
//	其余                                        → 控制 CLI
//
// 分发的**判据**不在这里：哪些名字算 client、什么条件才认领一次调用，是 shim
// 接管策略，属于 modules/wrapper（见那个包的 doc）。这里只剩「先问它，再问 CLI」。
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

	if code, ok := built.Wrapper().Dispatch(context.Background(), os.Args); ok {
		return code
	}

	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	build := cliapi.BuildInfo{
		Version: version, BuildTime: buildTime, CommitTime: commitTime,
	}
	cli := built.CLI()

	// argv0 分发：newgate-<preset> ≡ newgate --preset <preset> …（docs/08-operations.md）。
	// preset 名从 basename 第一个 `-` 之后取，不再切分（preset 名可含 `-`）。
	if strings.HasPrefix(name, "newgate-") {
		args := append([]string{"--preset", strings.TrimPrefix(name, "newgate-")},
			os.Args[1:]...)
		return cli.Run(args, build)
	}

	return cli.Run(os.Args[1:], build)
}

var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)
