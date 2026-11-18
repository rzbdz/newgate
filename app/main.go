package app

import (
	"context"
	"fmt"
	"os"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Options is the composition root's input: which graph to install, the version
// triple, and two optional hooks that let a product decide where assembly facts go.
//
// A nil Loader means the kernel's own (empty) graph — the kernel's binary and tests
// that only want "what does the framework do" take that path.
type Options struct {
	Loader     modules.Loader
	Version    string
	BuildTime  string
	CommitTime string

	// Trace decides where the assembly record goes. Nil = nowhere.
	//
	// It is a hook and not a kernel default because "which file, what format,
	// dedupe repeated identical assemblies or not" is a product decision, not a
	// mechanism. The kernel used to hardcode the distribution's log path here,
	// which meant the framework could not be used without a distribution and the
	// distribution could not change its log policy.
	Trace func() (stop func())

	// OnAssemblyFailure, if set, is called with the assembly error before Main
	// prints it and returns 70. Nil = just print.
	OnAssemblyFailure func(error)
}

// Main is the whole composition root, in order:
//
//  1. trace assembly (opts.Trace) — who was installed, in what order, every process;
//  2. start the graph — failure is 70, never run on half a graph;
//  3. ask the entry ledger — nobody claiming the call is a human sentence and 69,
//     not a crash.
//
// Why this lives in the kernel instead of each product's main: **it knows no
// module**, so it is identical for every consumer. Copying it into a
// distribution's main would let the two drift — and the symptom would be "the
// distribution lost its assembly record / returns a different exit code", the
// hardest kind of difference to notice. Each main is therefore only "which graph,
// which version".
//
// The graceful handoff is where the record matters most (the new process assembles
// in the background while the user sees nothing), so it is recorded here
// unconditionally whenever a product supplies a tracer.
func Main(ctx context.Context, opts Options) int {
	stopTrace := func() {}
	if opts.Trace != nil {
		stopTrace = opts.Trace()
	}
	defer stopTrace()

	built, err := New(ctx, opts.Loader)
	if err != nil {
		if opts.OnAssemblyFailure != nil {
			opts.OnAssemblyFailure(err)
		}
		fmt.Fprintln(os.Stderr, "newgate:", err)
		return 70
	}
	defer built.Stop(context.Background())

	// The facts of this invocation: argv0 normalization and version injection both
	// happen in MakeProcess, once.
	p := entry.MakeProcess(os.Args, os.Environ(), opts.Version, opts.BuildTime, opts.CommitTime)

	// The first log line: **who this invocation routes to**. It is the only record
	// of "I typed newgate, why did another shell start" — the criterion is declared
	// by modules, not guessed here.
	//
	// The entry ledger is the composition root's **own dependency**, so "nobody provides it" is
	// this package's problem, said in this package's words: an assembly without it cannot answer
	// the one question a process exists to answer. It is not a list of unremovable modules
	// (deleted 2026-11-18, when the kernel stopped shipping a module table) — it is the only Get
	// the root performs, and it says "I cannot start" instead of panicking.
	registry, ok := modules.Get(built.Context(), entry.Capability)
	if !ok {
		fmt.Fprintln(os.Stderr, "newgate:", i18n.T(
			"nobody provides the entry ledger ({port}) — this assembly has no module that can claim a call",
			i18n.A{"port": modules.Name(entry.Capability)}))
		return 70
	}
	handler, why, ok := registry.Resolve(p)
	if !ok {
		fmt.Fprintln(os.Stderr, "newgate: "+why)
		return 69
	}
	modules.Tracef("%s", i18n.T("entry resolve: argv0=\"{argv0}\" → {name} ({why})",
		i18n.A{"argv0": p.Argv0, "name": handler.Name(), "why": why}))
	return handler.Handle(p)
}
