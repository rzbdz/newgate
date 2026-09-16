// Command newgate is the entry point for both the control CLI and, via a
// PATH shim, the wrapper that injects environment for an agent.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agents "github.com/rzbdz/newgate/go/modules/builtin"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/api"
)

// main 做 argv0 分发：被当成某个 agent 调用时进 wrapper，被当成
// newgate-<preset> 调用时按 preset 覆盖进控制 CLI，否则进控制 CLI。
func main() {
	os.Exit(run())
}

func run() int {
	app, err := agents.New(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "newgate:", err)
		return 70
	}
	defer app.Stop(context.Background())

	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if a, ok := app.Get(name); ok {
		return runWrapper(a, os.Args)
	}
	cli := app.CLI()
	build := cliapi.BuildInfo{
		Version: version, BuildTime: buildTime, CommitTime: commitTime,
	}

	// argv0 分发：newgate-<preset> ≡ newgate --preset <preset> …（docs/03 §1.5）。
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
