// Command newgate is the entry point for both the control CLI and, via a
// PATH shim, the wrapper that injects environment for an agent.
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/ui/cli"
)

// main 做 argv0 分发：被当成某个 agent 调用时进 wrapper，被当成
// newgate-<preset> 调用时按 preset 覆盖进控制 CLI，否则进控制 CLI。
func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if a, ok := agents.Get(name); ok {
		runWrapper(a, os.Args)
		return
	}
	cli.Version = version
	cli.BuildTime = buildTime
	cli.CommitTime = commitTime

	// argv0 分发：newgate-<preset> ≡ newgate --preset <preset> …（docs/03 §1.5）。
	// preset 名从 basename 第一个 `-` 之后取，不再切分（preset 名可含 `-`）。
	if strings.HasPrefix(name, "newgate-") {
		args := append([]string{"--preset", strings.TrimPrefix(name, "newgate-")},
			os.Args[1:]...)
		os.Exit(cli.Run(args))
	}

	os.Exit(cli.Run(os.Args[1:]))
}

var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)
