package app

import (
	"context"

	modules "github.com/rzbdz/newgate/component"
)

// App is the ownership object of the process-level composition root.
//
// # Who it knows
//
// **Not a single module.** That is its definition, not a style choice: the composition root
// must commute with the "remove a module" operation -- if the user takes any non-built-in
// module out of the list, this package must still compile, still assemble, still run (all that
// is missing is what that module provides). The moment one line of import of
// `modules/<some business module>` appears here, that commutativity is gone, and the symptom is
// subtle: it blows up only at **compile time**, and whoever removes a module usually works at
// the configuration layer.
//
// The check is mechanical, and lives in app/direction_test.go:
//
//	command grep -rn '"github.com/rzbdz/newgate/modules' app/*.go
//	→ no line allowed (the one exception, the shared leaf config/paths, is not a dependency)
//
// # How it hands over this invocation
//
// It knows neither cli nor wrapper: both (and the future web-daemon) declare themselves in
// their own Start to the entry ledger of **modules/entry** (the only module the kernel knows,
// see modules/entry), and here we ask the ledger exactly once. "Who is the entry point" thus
// becomes the module's own knowledge, and swapping the UI is zero change at the composition
// root.
type App struct{ manager *modules.Manager }

// New builds and starts the component graph; success means all required ports are resolved and
// all components have started.
//
// Passing no loader = the kernel's own graph (Loader{}). Passing nil is the same -- callers
// commonly write "if opts.Loader is nil use the default", and catching it here is better than
// every caller catching it itself.
//
// Passing your own loader is the front door for **a consumer group's own graph**: the
// composition root knows no modules, only "who handed me which components, in what order".
func New(ctx context.Context, loaders ...modules.Loader) (*App, error) {
	live := make([]modules.Loader, 0, len(loaders))
	for _, l := range loaders {
		if l != nil {
			live = append(live, l)
		}
	}
	if len(live) == 0 {
		live = append(live, Loader{})
	}
	manager, err := modules.NewContext(ctx, live...)
	if err != nil {
		return nil, err
	}
	return &App{manager: manager}, nil
}

// Context exposes the read-only port table so the real composition root can get the final entry
// without recreating the service.
func (a *App) Context() modules.Context { return a.manager.Context() }

// ComponentNames returns the actual topological start order, for tests and runtime diagnostics.
func (a *App) ComponentNames() []string { return a.manager.ComponentNames() }

// Components returns the component definitions in start order (including Type).
func (a *App) Components() []modules.Component { return a.manager.Components() }

// Stop hands the whole application lifecycle back to the Manager to wind down in reverse order.
func (a *App) Stop(ctx context.Context) error { return a.manager.Stop(ctx) }
