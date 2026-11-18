// Package app is the composition root of the newgate executable.
//
// It knows **no module**: which components exist, what they need and in what order they start
// all arrive through `modules.Loader`. A distribution is another Go module that hands its own
// generated table to app.Options.Loader (see newgate-ext's manifest package); the kernel's own
// binary and `go test ./...` get Loader{}, which installs **nothing** -- a framework, not a
// downgraded product.
//
// App holds the Manager and exposes to main only the entry points really needed after startup.
package app

import modules "github.com/rzbdz/newgate/component"

// Loader is the kernel's own assembly: the empty one.
//
// It exists so "nobody chose a graph" has a value to hand over -- the kernel ships no module
// (TestKernelShipsNoModules), and a third-party composition root replaces this Loader with its
// own table without touching the framework.
type Loader struct{}

// Load returns no components.
func (Loader) Load() ([]modules.Component, error) { return nil, nil }
