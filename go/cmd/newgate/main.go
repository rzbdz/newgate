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

// main 做 argv0 分发：被当成某个 agent 调用时进 wrapper，否则进控制 CLI。
func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if a, ok := agents.Get(name); ok {
		runWrapper(a, os.Args)
		return
	}
	cli.Version = version
	cli.BuildTime = buildTime
	cli.CommitTime = commitTime
	os.Exit(cli.Run(os.Args[1:]))
}

var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)
