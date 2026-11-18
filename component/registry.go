package component

import "sync"

// Registry is a general ledger for "register → undo" contributions: a monotonic token, exact
// undo by token, and an optional admission check.
//
// # Why it exists
//
// Cross-module injection converged onto one path on 2026-09-17: **to insert something into
// someone else's extension point, consume its service and call RegisterX**, no longer
// `Provides: Provide(someone else's capability, value)` (that path has no Release, no duplicate
// check, no lifecycle -- two modules claiming the same command name silently win first-come,
// first-served). After that convergence, "register a table with undo" had to be written once in
// every owner, so it was pulled out here.
//
// Relation to those hand-written ledgers in the product layer: they **stay as they are for
// now**. Each of them has a real difference (plugin topological sort with copy-on-write, a read
// side with Refresh, several heterogeneous registries sharing one token table), and forcing
// them together would produce a worse abstraction. This one is validated in one place first,
// and then they migrate one by one.
//
// Compared with those three implementations, this one is missing a `tokens map[string]uint64`:
// that table was there to tolerate "duplicate registration of the same name" -- the new value
// overwrites the old and the old value's Release becomes a no-op. We **forbid** duplicate
// registration (delegated to the caller's check), duplicates error on the spot, so all that is
// needed is a monotonic counter.
//
// Usable as a zero value: a new module service can just embed a Registry[T] field, no
// construction needed.
type Registry[T any] struct {
	mu    sync.Mutex
	items []registryEntry[T]
	next  uint64
}

type registryEntry[T any] struct {
	token uint64
	value T
}

// Register first hands value to check for review, and only adds it to the ledger if it passes,
// returning an undo handle that belongs only to this registration.
//
// check is called **under the write lock**, so "duplicate check" and "insert" are atomic with
// respect to concurrent registration: if two goroutines register the same name at the same
// time, the second is guaranteed to see the first. check receives a **snapshot** of all current
// contributions (modifying it does not affect the ledger) and a non-nil return means rejection,
// in which case the ledger is unchanged and Release is nil.
//
// Passing nil for check means no check (diagnostics, contributions that can stack and have no
// key namespace, are like this).
func (r *Registry[T]) Register(value T, check func([]T) error) (Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if check != nil {
		if err := check(r.snapshotLocked()); err != nil {
			return nil, err
		}
	}

	r.next++
	token := r.next
	r.items = append(r.items, registryEntry[T]{token: token, value: value})

	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, item := range r.items {
			if item.token != token {
				continue
			}
			r.items = append(r.items[:i], r.items[i+1:]...)
			return nil
		}
		// Stale Release: this entry was already undone (or never inserted). Returning nil instead of
		// an error is to make Stop idempotent -- a component's Stop may be called more than once, and
		// an extra "it is already gone" on the release path should not be treated as a fault.
		return nil
	}, nil
}

// All returns a snapshot of every contribution, in registration order.
//
// What is returned is a copy: other modules may be registering while the caller iterates over
// it, so the internal slice must not be handed out. An empty ledger returns nil (not an empty
// slice), and callers can just test len()==0.
func (r *Registry[T]) All() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *Registry[T]) snapshotLocked() []T {
	if len(r.items) == 0 {
		return nil
	}
	out := make([]T, 0, len(r.items))
	for _, item := range r.items {
		out = append(out, item.value)
	}
	return out
}
