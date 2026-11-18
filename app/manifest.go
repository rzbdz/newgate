package app

import (
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Entry is a component's **identity in the assembly list**: directory name + component
// definition.
//
// Why the identity is the directory name and not the component name: the directory name is the
// key for "install which / disable which". The distribution spec writes directory names in
// disable (the distribution has `claudecode_deepseek`), and the component name need not match
// it (`claudecode-deepseek`). Disabling by component name **silently fails** (the names do not
// match and nobody reports an error), so the key comes from the scan (tools/genmodules scans
// directory names) and is carried here as-is. On the kernel side everything happens to share
// the same name today -- a coincidence, not a rule.
type Entry struct {
	Dir       string
	Component modules.Component
}

// CoreModules returns **all components shipped with the kernel**, sorted by directory name; the
// implementation lives in the build-time generated modules_gen.go.
//
// Why this exit exists: the assembly logic of the composition root stays in the kernel, but
// "which ones to install" has been **the consumer's business** since 2026-09-20 (a distribution
// installs its own modules and turns off a few kernel ones as needed). The consumer needs a
// table with identity -- a bare list of components cannot be disabled by directory name, and a
// typo cannot be validated before assembly.
//
// The implementation must be generated: non-test sources in app must not import any business
// module (see direction_test.go), and this list names every module -- modules_gen.go is its
// only legal home.
func CoreModules() []Entry { return coreTable() }

// coreTable is the swappable view over the generated table.
//
// Why a seam at all: the kernel ships no modules, so every rule about how Selection
// treats a module table (names must hit, the root's own port cannot be disabled,
// AllCore grows with the table) is only testable against a table that has modules
// in it. Production reads the generated coreModules(); tests swap this and restore.
var coreTable = coreModules

// swapCoreEntries replaces the kernel table for a test and returns the restore.
func swapCoreEntries(replace func() []Entry) func() {
	prev := coreTable
	coreTable = replace
	return func() { coreTable = prev }
}

// Selection is a set of components **decided by the consumer**: the kernel's own minus the
// disabled, plus Extra.
//
// The distribution uses it to hand its own modules to the kernel's composition root:
//
//	app.Main(ctx, app.Options{Loader: app.Selection{
//	    Disable: []string{"cli"},                       // disable the kernel's UI
//	    Extra:   []app.Entry{{Dir: "simple-cli", Component: simplecli.New()}},
//	}})
//
// Zero change on the kernel side: the composition root still knows no module, only this table.
type Selection struct {
	// AllCore disables **every disableable module shipped with the kernel** -- "this distribution
	// wants only the framework + my own modules".
	//
	// Why a field rather than making Disable list every directory: the exhaustive spelling costs
	// two things, and both are silent. First, **the list rots** -- it is a second copy of
	// CoreModules(), so when the kernel adds a module the list does not grow with it, and a
	// distribution that "thought it turned everything off" quietly installs one more. Second, it
	// turns a product decision that fits in one sentence into a dozen lines of data (the skeleton
	// distribution is that). AllCore says "none of the kernel's own", and it grows together with
	// CoreModules().
	//
	// "Disableable" is the key word: the port the composition root itself needs (the entry ledger)
	// **cannot be disabled by whoever provides it**, and AllCore cannot disable it either --
	// otherwise a distribution could turn itself into a binary where nobody can answer "who owns
	// this invocation". That is not an exception; it is the boundary of the definition of
	// "disableable" (see compositionRootPorts).
	//
	// Giving it together with Disable is an error: the two say the same thing (those names are
	// already inside AllCore).
	AllCore bool
	// Disable is the kernel module to turn off, written as the **directory name**. The name must
	// match -- a miss is an error.
	//
	// To turn everything off use AllCore (write `"disable": ["*"]` in the spec, see
	// tools/distgen).
	Disable []string
	// Extra is the consumer's own modules, identical in shape to kernel modules (directory name +
	// New()).
	Extra []Entry
}

// Load implements modules.Loader.
//
// Every rejection here is loud, for the same reason: **mistakes at this layer all disguise
// themselves as other problems**. A misspelled disable name looks exactly like "turned off" (in
// fact it is "I thought it was off, it is still running"); a duplicate assembly only shows up
// at graph-construction time as `duplicate component`, by which point it is far from "I wrote
// one extra entry in the list".
func (s Selection) Load() ([]modules.Component, error) {
	core := CoreModules()
	known := make(map[string]Entry, len(core))
	for _, e := range core {
		known[e.Dir] = e
	}
	extra := make(map[string]bool, len(s.Extra))
	for _, e := range s.Extra {
		if e.Dir == "" {
			return nil, i18n.E("assembly selection: an Extra component has no directory "+
				"name (component name \"{name}\") — the directory name is what disables "+
				"it and what names it in errors", i18n.A{"name": e.Component.Name})
		}
		extra[e.Dir] = true
	}

	off := make(map[string]bool, len(core))
	if s.AllCore {
		// When two spellings say the same thing it must be an error, not a union: otherwise `AllCore`
		// and a long list of Disable would both exist, and the latter is a stale copy of the former
		// -- exactly what this field exists to remove.
		if len(s.Disable) > 0 {
			return nil, i18n.E("assembly selection: AllCore and Disable were both given "+
				"({list}) — the former already contains the latter. Keep only AllCore to "+
				"have both; leave AllCore out to disable just a few", i18n.A{"list": s.Disable})
		}
		for _, e := range core {
			if _, serves := servesCompositionRoot(e.Component); serves {
				continue // the one that cannot be turned off is not among the "turnable off"
			}
			off[e.Dir] = true
		}
	}
	for _, dir := range s.Disable {
		e, isCore := known[dir]
		if !isCore {
			if extra[dir] {
				return nil, i18n.E("assembly selection: disable names \"{dir}\", but that is "+
					"a module from Extra — Extra is the table you supplied yourself, so leave "+
					"it out if you do not want it (disable only turns off kernel modules; "+
					"listing it here has no effect, and you probably think it did)",
					i18n.A{"dir": dir})
			}
			return nil, i18n.E("assembly selection: disable names \"{dir}\", but neither the "+
				"kernel nor your own table has this module (did you write the directory "+
				"name? e.g. my_module, not my-module; use AllCore to turn everything off)",
				i18n.A{"dir": dir})
		}
		if port, serves := servesCompositionRoot(e.Component); serves {
			return nil, i18n.E("assembly selection: disable names \"{dir}\" (component "+
				"{name}), but it provides the port {port} that the composition root itself "+
				"needs — turn it off and nothing in the process can answer \"who claims this "+
				"call\". What makes it unremovable is not a list, it is the composition "+
				"root's own dependency (see compositionRootPorts)",
				i18n.A{"dir": dir, "name": e.Component.Name, "port": port})
		}
		off[dir] = true
	}

	// The order is not a dependency declaration: the real start order is computed from the
	// capability graph at graph-construction time (see the package comment).
	out := make([]modules.Component, 0, len(core)+len(s.Extra))
	owner := make(map[string]string, len(core)+len(s.Extra))
	add := func(dir string, c modules.Component) error {
		if prev, dup := owner[c.Name]; dup {
			return i18n.E("assembly selection: component \"{name}\" is installed twice "+
				"(from {first} and {second}) — two components with the same name only fail "+
				"at graph build time",
				i18n.A{"name": c.Name, "first": prev, "second": dir})
		}
		owner[c.Name] = dir
		out = append(out, c)
		return nil
	}
	for _, e := range core {
		if off[e.Dir] {
			continue
		}
		if err := add(e.Dir, e.Component); err != nil {
			return nil, err
		}
	}
	for _, e := range s.Extra {
		if err := add(e.Dir, e.Component); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, i18n.E("assembly selection: not a single component (did disable turn "+
			"off every kernel module?) — an empty graph only looks like a broken command", nil)
	}
	return out, nil
}

// compositionRootPorts are **the ports the composition root itself needs**: the ones it must
// get once the graph is up.
//
// There is only one today -- the entry declaration ledger (the first thing app.Main does after
// the graph is up is ask it "who owns this invocation"). This is not a "list of modules that
// cannot be removed" but this package's own dependency list: whoever provides these ports
// cannot be removed (see Load), because removing it means the composition root cannot get what
// it needs to stay alive.
//
// The boundary must be in **this** direction: the mechanism does not know module names. Before
// 2026-09-20 this said `case dir == "root"` -- "which module cannot be removed" was thus
// hardcoded into the kernel once, and declared once more by the module itself with
// `Type = builtin`; the two places said the same thing in their own ways, and changing one
// would not make the other go red. Now there is only one statement (the composition root's
// dependency), and the Type marker is only used by the removal matrix.
//
// It must also stay **short**: every entry added here costs a bit of module replaceability. Do
// not add unless you must.
func compositionRootPorts() []string {
	return []string{modules.Name(entry.Capability)}
}

// servesCompositionRoot reports whether a component provides a port the composition root itself
// needs; if so it returns the port name along with it (errors use it to name "what is missing",
// not "which module").
func servesCompositionRoot(c modules.Component) (string, bool) {
	for _, provision := range c.Provides {
		for _, port := range compositionRootPorts() {
			if provision.Name() == port {
				return port, true
			}
		}
	}
	return "", false
}
