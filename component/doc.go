/*
Package component is newgate's dependency and lifecycle kernel.

# Why it is needed

newgate's features are not a fixed call tree. Clients, model families, CLI commands and request
patches can all be added independently; some of those behaviors only hold when two modules exist
at the same time. If modules directly import each other's concrete implementations, you end up
with import cycles, a central registry, and package init that cannot be undone.

The minimal model used here is: modules declare "what capability I need, what capability I
provide", the composition root only lists the component set, and the Manager computes the start
order from the declarations. The framework knows nothing of Config, Gateway or Claude Code; it
only knows the six structural concepts below.

# The six concepts

The kernel builds a directed graph out of six concepts:

  - Capability[T] is the typed name of a service or extension point. NewCapability[T] declares
    it; a port can have zero, one or more providers -- cardinality is not the framework's
    constraint, whoever needs "only one" asserts it at their own Get call site or in their
    business logic.
  - Requirement says a component consumes a capability. Need is a **strong dependency**: the
    provider must exist, otherwise assembly fails. Optional is a **weak dependency**: you exist,
    so I depend on you (ordering included); you do not, so I do not (no edge, no error, just no
    port). Both are ordering edges, so "whoever registers things into someone else" always comes
    after "the side being registered into".
  - Provision binds a capability to the concrete value it provides.
  - Component is a graph node: it can consume and provide capabilities, starts after its
    providers, and stops before them.
  - Context is the resolved port table passed to Start. Get reads a single-value port, GetAll
    reads every contribution of a multi-value port.
  - Manager validates the graph, starts nodes in dependency order, and stops them in the reverse
    order.

The key point is "a component is not a service". Component is only a lifecycle and dependency
declaration; what is actually called across modules is the T in Capability[T]. The consumer
below starts after the provider without needing to know which component provides Store:

	var StoreCapability = component.NewCapability[Store]("config.store")

	provider := component.Component{
		Name: "config",
		Provides: []component.Provision{
			component.Provide(StoreCapability, store),
		},
	}

	consumer := component.Component{
		Name: "cache",
		Requires: []component.Requirement{
			component.Need(StoreCapability),
		},
		Start: func(ctx context.Context, graph component.Context) error {
			store := component.MustGet(graph, StoreCapability)
			return startCache(ctx, store)
		},
	}

# Lifecycle

Building a Manager has five steps:

 1. Collect all Components from the Loader.
 2. Validate names, capability types and required providers.
 3. Topologically sort so providers come before consumers.
 4. Call Start in graph order.
 5. On a start failure or on Stop, call Stop in the reverse order.

**There are only two phases, Start and Stop**, no "injection phase". Port bindings (Provides)
are all done **before any Start**, so the only thing affected by the start order is "who
registered what in whose Start" -- and that is guaranteed by the dependency edges: registrants
come after the owner. Stopping is the strict reverse of starting, so a registrant's Stop always
precedes the registered side, and the target is still alive when the registration is undone.

(Before 2026-09-18 there was a second `Attach` phase for "injection edges that do not take part
in ordering"; it was deleted along with that edge. Why it was deleted and what it cost is
written in the Optional comment.)

Before Start only ports and values may be declared, no IO. Components obtain dependencies in
Start and register extensions there. The registration API returns a Release; consumers keep
those handles and call ReleaseAll in Stop. That way unloading only removes the contributions
this component owns and never deletes newer values registered later.

# A process you can see

The assembly process (which components were scanned, what each declared, which weak dependency
did not hit, what order came out, how long each component took to start) is reported via
SetTrace. The kernel knows nothing of logging, it only provides the exit; whether and what to
install is the composition root's decision (`app.TraceToLogFile` appends each process's segment
to the newgate log, so the graceful-handover upgrade can be compared line by line against the
old and new versions afterwards).

# Boundary

The kernel deliberately knows nothing about configuration files, HTTP, models, clients or
commands. Those concepts belong to their modules; to the kernel they are just ordinary typed
capability values.
*/
package component
