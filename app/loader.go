// Package app is the composition root of the newgate executable.
//
// Modules only declare their own capabilities, dependencies and lifecycle; they should not know
// "which modules are enabled by default in the product". The assembly list is generated at
// build time by tools/genmodules scanning modules/ (see modules_gen.go), and component.Manager
// then computes the real start order from Need/Provide; the list order is not a hidden
// dependency.
//
// **Installing a module = copying the directory into modules/ and recompiling**; no list needs
// editing. The check is in tools/genmodules: the directory has a root module.go and exports
// `func New() modules.Component`.
//
// App holds the Manager and exposes to main only the entry points really needed after startup.
// Alternative products or tests can use a different Loader without modifying any business
// module.
package app

import (
	modules "github.com/rzbdz/newgate/component"
)

//go:generate go run github.com/rzbdz/newgate/tools/genmodules

// Loader is the kernel's own assembly: **whatever the scan finds**, nothing more.
//
// It is empty on purpose. A composition root that wants a product passes an
// app.Selection (a distribution does); Loader{} is what `go test ./...` and the
// kernel's own binary see when nobody has chosen anything.
type Loader struct{}

// Load returns the component set; a third-party composition root can replace this
// Loader without touching the framework.
//
// "The graph is the scanned list" stays an equation with no exceptions: the entry
// ledger used to be installed explicitly by the composition root, which made it the
// one module the root knew by name. It is a normal scanned module now, and what
// makes it unremovable is not this file but the root's own dependency on its port
// (see compositionRootPorts in manifest.go).
func (Loader) Load() ([]modules.Component, error) {
	return generatedComponents(), nil
}
