// Command newgate is the **kernel's own** binary: the process composition root
// with no modules installed at all.
//
// Its only reason to exist is that the kernel can be built and tested on its own.
// **It is not a product**: a real binary installs a graph chosen by a distribution
// (another Go module that depends on this one and passes its own loader). Running
// this build serves nothing — literally: there is no gateway, no CLI, no data
// plane, because the kernel ships none (2026-11-18).
//
// The composition root's logic (trace → start the graph → ask the entry ledger →
// hand off) all lives in app.Main; this file only says "which graph, which
// version". A distribution's main is the same few lines.
package main

import (
	"context"
	"os"

	app "github.com/rzbdz/newgate/app"
)

func main() {
	os.Exit(app.Main(context.Background(), app.Options{
		Version:    version,
		BuildTime:  buildTime,
		CommitTime: commitTime,
	}))
}
