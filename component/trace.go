package component

import (
	"sort"
	"strings"
	"sync"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

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

// describeComponent renders one declaration as a line of facts:
//
//	breaker            type=infra   provides=[breaker]    needs=[~cli]
//
// The `~` prefix marks a **weak dependency** (Optional, see Requirement): installed means it is
// depended on, not installed means there is no such edge. This line is the first scene when
// investigating "why did the module not start" -- it is a static declaration, independent of
// the runtime result, so it can be compared directly with the sorted result below.
func describeComponent(c Component) string {
	provided := make([]string, 0, len(c.Provides))
	for _, p := range c.Provides {
		provided = append(provided, p.spec.name)
	}
	needed := make([]string, 0, len(c.Requires))
	for _, r := range c.Requires {
		name := r.spec.name
		if r.optional {
			name = "~" + name
		}
		needed = append(needed, name)
	}
	if len(provided) == 0 {
		provided = append(provided, "-")
	}
	if len(needed) == 0 {
		needed = append(needed, "-")
	}
	sort.Strings(provided)
	sort.Strings(needed)
	// `type=` / `~` are machine markers (graph-construction criteria and field names), kept
	// outside the message.
	return pad(c.Name, 20) + " type=" + pad(string(c.Type), 9) +
		i18n.T(" provides=[{list}]", i18n.A{"list": strings.Join(provided, " ")}) +
		i18n.T(" needs=[{list}]", i18n.A{"list": strings.Join(needed, " ")})
}

// pad right-pads with spaces by display width. Component names are ASCII (directory names), so
// there is no need to handle CJK double width here -- layout is lib/style's business, and the
// kernel does not pull it in.
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
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
