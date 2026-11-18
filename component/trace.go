package component

import "sync"

// Trace is the exit for **graph-construction events**: the kernel reports facts like "how many
// components were scanned, what each declared, who is ordered before whom, how long each took
// to start", and how to lay it out and where to write it is the product layer's decision.
//
// Why it is in the kernel instead of the product layer counting for itself: only resolve and
// the lifecycle loop know these facts -- declaration order, skipped weak dependencies,
// topological order, each component's start/stop duration. The product layer cannot get them
// (the Manager only hands out the result, not the process), so "why did this module not start"
// and "why is the order like this" could only be guessed.
//
// The kernel knows nothing of logging, files or environment variables: the product layer
// installs a function (see app.TraceTo). Default nil = report nothing.
type Trace func(format string, args ...any)

var (
	traceMu sync.RWMutex
	traceFn Trace
)

// SetTrace installs the graph-construction event exit; pass nil to turn it off.
//
// A process-level setting, the same kind as thinkcache.SetErrorHandler / buildinfo.Set: it is
// the one thing "where this process reports diagnostics", not some component's capability. It
// must be called **before** assembly to be complete (reporting starts as soon as assembly
// begins).
func SetTrace(fn Trace) {
	traceMu.Lock()
	traceFn = fn
	traceMu.Unlock()
}

// tracef reports one line. It is a no-op when no exit is installed, so call sites need no nil
// check.
func tracef(format string, args ...any) {
	traceMu.RLock()
	fn := traceFn
	traceMu.RUnlock()
	if fn != nil {
		fn(format, args...)
	}
}

// Tracef lets the **composition root** write a line into the assembly log too (the kernel's own
// tracef is package-private).
//
// There is exactly one situation where the composition root needs it, but it matters: **entry
// resolution** -- "who is this process invocation routed to" must land in the same log as the
// assembly process, otherwise investigating "I typed newgate, why did some other shell start"
// means digging in two places at once. It introduces no new destination: the exit is still the
// one from SetTrace.
func Tracef(format string, args ...any) { tracef(format, args...) }
