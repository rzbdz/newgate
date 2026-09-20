<div align="center">

# newgate

### The mechanism for a gateway that picks the model — with no product in it.

`heavy` · `normal` · `mid` · `light` · `vision`

[![CI](https://github.com/rzbdz/newgate/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)
![build](https://img.shields.io/badge/build-fully_offline-blue)

</div>

---

The kernel is what makes adapting cheap: tiers, fallbacks, the breaker and the
upgrade path are **mechanism**, so a new model or a new upstream quirk is a
module someone adds in their own distribution — not a change to the core.

**Your CLI asks for a tier; the gateway picks the model.**
`heavy` is not one upstream, it's an ordered candidate chain — your rules first,
then predicted time-to-first-byte. The response tells you where it went.

**Nothing dies with a single upstream.**
Every skip is explained and every reroute is logged. A provider being down,
rate-limited or wrong is a routing decision, not an outage.

**A breaker that knows whose fault it was.**
Availability, rate-limit and config failures are separate ledgers with separate
thresholds. A 400 caused by the *shape of your request* is counted against
nobody. Half-open trials recover on their own.

**Restart without dropping a request.**
The listening socket is handed to the new process and the old one drains what's
in flight, streams included. Upgrades are safe mid-session.

**Everything is a module — including the parts you'd expect to be special.**
Gateway, breaker, UI, entry point, message catalog. Coupling is by capability,
nothing imports its peers, and "cannot be removed" is derived from the port the
composition root consumes — never from a name list.

**Zero dependencies, fully offline build.**
No third-party packages, no `go.sum`; the suite passes with `GOPROXY=off`. CJK
width, message catalogs and terminal layout are hand-written.

## What is *not* here

No upstream quirks, no client takeover, no tiers anyone chose. Which modules
ship, and in what order, is a distribution's decision — its own repository, its
own module list, its own policies, forked and edited by whoever wants them:

```go
app.Main(ctx, app.Options{Loader: app.Selection{
    Disable: []string{"cli"},                                        // by directory name
    Extra:   []app.Entry{{Dir: "deepseek", Component: deepseek.New()}},
}})
```

Nothing in the kernel branches on a product name — `deepseek` appears only in
doc comments, tests and dev tools. Build one from
[**rzbdz/newgate-ext**](https://github.com/rzbdz/newgate-ext), or write your own
selection and compose the gateway you actually want.

## Quick start

```bash
make build            # → bin/newgate — kernel modules only: a mechanism harness
bin/newgate init && bin/newgate start
bin/newgate doctor    # when something is wrong
```

## Docs

[docs/00-index.md](docs/00-index.md) — the map ·
[docs/03-architecture.md](docs/03-architecture.md) — ownership and the boundary
to distributions · [CLAUDE.md](CLAUDE.md) — the house rules a contributor
(human or agent) follows.

## License

Pre-1.0 and still moving. Ask before you build something you intend to depend on.
