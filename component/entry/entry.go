// Package entry is the contract for the **process entry** edge: once a process wakes up, who
// should this invocation go to.
//
// # Why it lives beside the kernel instead of inside some module
//
// To have a composition root that **cannot recognize any module**, there must be a boundary
// type both sides can see: the composition root on one side, modules on the other, they cannot
// import each other, and the only thing they can both see is the kernel. This type describes
// **the process** (how was I invoked), not **the business** (who am I, what do I do), so it is
// at the same level as Start/Stop -- the kernel provides the shape, the product fills in the
// content (the same rule as component.Type).
//
// # Why not "app asks wrapper first, then cli"
//
// That hardcodes the dispatch criterion into the composition root: who the entry is, and under
// what conditions it belongs to whom, is all module knowledge. Before 2026-09-20
// cmd/newgate/main.go was written that way, and the consequence was that the composition root
// imported the cli and wrapper modules, so deleting either one made it uncompilable -- "the
// user's modules can be removed" did not hold at the composition root.
//
// Now it is the other way around: **the entry is declared by contributors**. Each module
// Registers a Handler in its own Start and gives a rank (smaller is asked first), and the
// composition root does a single Resolve and then calls it.
//
//	rank 0    wrapper    argv0 is some taken-over client (forwarded by a PATH shim)
//	rank 1000 cli        the default entry: it is this one if nobody claimed the invocation
//	(future)  web-daemon with entry=web in the configuration
//
// So "swap in a better cli" and "have newgate start a web server directly" are **zero change**
// at the composition root: replace the modules/cli directory, or add a new module that declares
// a higher rank.
package entry

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// The three ranks used for declaration. Smaller is asked first, and these are **not magic
// numbers**, they read as priority bands:
//
//	RankShim      conditional entry: asked first; if the condition fails, later ones take over
//	RankPreferred explicitly preferred: the shell the product/tests chose (named in config, or
//	              the only one in a test build)
//	DefaultRank   fallback: always returns true, so it must be asked last
//
// The gaps between the bands are left for the future: splitting by environment and splitting by
// configuration both go in the middle, with no change to existing modules.
const (
	RankShim      = 0
	RankPreferred = 500
	DefaultRank   = 1000
)

// Process is every fact about "this process invocation", and the only shared vocabulary on both
// sides of the boundary.
//
// It has exactly one source: the composition root assembles it in main from os.Args /
// os.Environ and link-time variables. Modules must not read os.Args themselves -- doing so
// would copy "how the process was invoked" once more, which is exactly why this package exists.
type Process struct {
	Argv0      string   // basename of os.Args[0] (no .exe): `newgate` / `claude` / `newgate-ds`
	Args       []string // os.Args[1:]
	Env        []string // a verbatim copy of os.Environ()
	Version    string   // injected at link time; `dev` means nothing was injected
	BuildTime  string
	CommitTime string
}

// Handler is one entry declaration.
type Handler interface {
	// Name goes into the log: the composition root's `[entry] resolve: … → <Name>` line.
	Name() string

	// Claims says whether this invocation belongs to me. It **must only read Process and have no
	// side effects** -- it is asked before the actual dispatch, and other declarants may be asked
	// too.
	Claims(Process) bool

	// Handle executes this invocation and returns an exit code.
	Handle(Process) int
}

// Registry is the entry declaration ledger. It is provided by **the modules/entry module** (see
// modules/entry), the only module the kernel knows and the only one that cannot be removed: the
// user cannot remove it, so the invariant "someone is always in charge of the entry" is
// guaranteed by it.
type Registry interface {
	// Register declares one entry. Smaller rank is asked first (see DefaultRank).
	Register(h Handler, rank int) (component.Release, error)

	// Resolve asks every declarant and returns the first one that claims it. Why ok=false instead
	// of a panic when nobody claims it: a graph that installs only business modules and no UI at
	// all is **legal**, and in that case the composition root should print a human sentence and
	// exit, not crash.
	Resolve(p Process) (Handler, string, bool)

	// Handlers lists every declarant in rank order, for diagnostics like `newgate plugin` to
	// enumerate.
	Handlers() []Handler
}

// Table is the default implementation of Registry.
type Table struct {
	mu       sync.Mutex
	handlers []registration
	// next is a monotonic token, never a slice index: releasing an entry and then
	// registering another used to hand the new one a recycled seq, so the new
	// entry's Release deleted the wrong handler (register A, register B, release A,
	// register C → C's seq collides with B's). component.Registry[T] was built with
	// a counter for exactly this reason; this ledger predates it.
	next uint64
}

type registration struct {
	handler Handler
	rank    int
	seq     uint64
}

var _ Registry = (*Table)(nil)

func NewTable() *Table { return &Table{} }

func (t *Table) Register(h Handler, rank int) (component.Release, error) {
	if h == nil {
		return nil, errNilHandler()
	}
	if h.Name() == "" {
		return nil, errEmptyName()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.handlers {
		if r.handler.Name() == h.Name() {
			return nil, errDuplicate(h.Name())
		}
	}
	t.next++
	reg := registration{handler: h, rank: rank, seq: t.next}
	t.handlers = append(t.handlers, reg)
	t.sortLocked()
	return func() error {
		t.mu.Lock()
		defer t.mu.Unlock()
		for i, r := range t.handlers {
			if r.seq == reg.seq {
				t.handlers = append(t.handlers[:i:i], t.handlers[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// sortLocked sorts by rank, keeping declaration order among equal ranks (stable).
//
// Stability is not a detail: the name `newgate` may be claimed by two modules at once in the
// future, and then whoever declared first wins, rather than it being decided by map iteration
// order -- a bug like that only shows up on some machines.
func (t *Table) sortLocked() {
	sort.SliceStable(t.handlers, func(i, j int) bool {
		return t.handlers[i].rank < t.handlers[j].rank
	})
}

func (t *Table) Resolve(p Process) (Handler, string, bool) {
	t.mu.Lock()
	snapshot := append([]registration(nil), t.handlers...)
	t.mu.Unlock()
	asked := make([]string, 0, len(snapshot))
	for _, r := range snapshot {
		if r.handler.Claims(p) {
			return r.handler, i18n.T("claimed by: {chain}",
				i18n.A{"chain": joinAsked(asked, r.handler.Name())}), true
		}
		asked = append(asked, r.handler.Name())
	}
	return nil, i18n.T("no entry claimed this call (asked: {chain})",
		i18n.A{"chain": joinAsked(asked, "")}), false
}

func (t *Table) Handlers() []Handler {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Handler, 0, len(t.handlers))
	for _, r := range t.handlers {
		out = append(out, r.handler)
	}
	return out
}

func joinAsked(asked []string, winner string) string {
	out := ""
	for i, name := range asked {
		if i > 0 {
			out += " → "
		}
		out += i18n.T("{name} did not claim", i18n.A{"name": name})
	}
	if winner != "" {
		if out != "" {
			out += " → "
		}
		out += winner
	}
	if out == "" {
		return i18n.T("(nobody registered)", nil)
	}
	return out
}

// Capability is the port identity of the entry declaration ledger. It is provided by
// modules/entry (see modules/entry).
//
// The composition root (app) also imports this package -- it is the only module-side contract
// that may do so (see the opening section of this package).
var Capability = component.NewCapability[Registry]("entry")

// ---- The two kinds of mistake that can be made at declaration time ----
//
// They live here rather than at the call site: declaring is **a module's own programming
// error** (duplicate name, empty name, nil), and it must return an error right there so that
// module's Start fails and rolls back, instead of blowing up at dispatch time.
//
// All three are written as **functions** rather than package-level variables: package-level
// variables are evaluated during init, while the i18n translations are installed only at
// assembly time (modules/locale Installs them in Start), so at init time you always get the
// source-language sentence -- a bug that only shows up in non-English environments.
func errDuplicate(name string) error {
	return i18n.E("entry: a handler with this name is already registered: {name}",
		i18n.A{"name": name})
}

func errNilHandler() error {
	return i18n.E("entry: handler must not be nil", nil)
}

func errEmptyName() error {
	return i18n.E("entry: handler must have a Name()", nil)
}

// MakeProcess assembles "how the process was invoked" into a Process.
//
// The version lands on the process itself (Process.Version etc.), **no longer written into some
// global leaf**: that leaf (buildinfo) belongs to the distribution's modules, and the kernel
// should not know it. Whoever needs the version asks Process for it.
//
// **Only the composition root should call it**, and only once, in main. It does two things that
// must happen as a pair: normalize argv0 (basename, strip .exe, strip the path) and inject the
// version -- both are "facts about this binary on this run", and modules reading their own
// copies would drift.
func MakeProcess(argv []string, env []string, version, buildTime, commitTime string) Process {
	name := ""
	var args []string
	if len(argv) > 0 {
		name = strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
		args = argv[1:]
	}
	return Process{
		Argv0:      name,
		Args:       args,
		Env:        env,
		Version:    version,
		BuildTime:  buildTime,
		CommitTime: commitTime,
	}
}
