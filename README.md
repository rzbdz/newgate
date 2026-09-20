<div align="center">

# newgate

### Your AI CLI asks for a **tier**. newgate picks the model.

`heavy` · `normal` · `mid` · `light` · `vision`

[![CI](https://github.com/rzbdz/newgate/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)
![build](https://img.shields.io/badge/build-fully_offline-blue)

</div>

---

One local endpoint. Your clients point at it once; which provider and model
answers is a decision you wrote down per tier — with fallback order, health
tracking and counters you can actually read.

## See it

```console
$ newgate tier normal
newgate tier  chain head ds (default) · max attempts 5 · budget 2m0s
  Final    smt-deepseek/deepseek-flash
  1. smt-deepseek/deepseek-flash    profile ds · fast 1224ms
  2. ark/ark-code-latest            profile ark · fast 1532ms
  · 15 candidates skipped
  Reason       Count  Note
  excluded         8  an excluded profile is only used when selected explicitly;
                      it never joins automatic ordering
  unavailable      7  the provider was rejected as unavailable — see newgate metrics / probe
```

```console
$ newgate metrics
    33req/0err · counters reset when the daemon restarts
model health
    7 usable (6 fast · 1 usable) · 2 laggy · 9 unavailable
  · smt-deepseek/deepseek-flash
        fast · ≤4K 1224ms/128 samples · ≤128K 230ms/211 samples
request counters
  group     counter                                count  description
  requests  requests.total                            33  requests entering the gateway
  plugin    special.classifier-naked.shortcircuit     15  naked: the classifier request is
                                                          approved directly, the upstream is
                                                          not called
```

## What you get

| | |
| --- | --- |
| **Tiers, not model names** | Clients ask for `normal`; you decide what that means today. Change it once and every client follows — no config touched, no session restarted. |
| **Fallback that explains itself** | Candidate chains per tier, ordered by your rules and then by predicted time-to-first-byte. Every skip is reported, every reroute is logged, and the response header says where it went. |
| **A breaker that knows what to forget** | Availability, rate-limit and config failures are different ledgers with different thresholds. A 400 caused by *your request's shape* is not the provider's fault — it's counted, never held against them. Half-open trials recover on their own. |
| **Metrics you can act on** | Latency by context size, failovers, first-byte timeouts, rewrites, cancellations. Each counter has a one-line explanation, because a number nobody can interpret is decoration. |
| **Takeover nobody notices** | A PATH shim plus the client's own config rewritten in place, with byte-exact backups. The client keeps its dialect and never learns. One binary serves every client — `argv[0]` decides which. |
| **Restart without dropping a request** | The listening socket is handed to the new process; the old one drains what's in flight, streams included. Upgrading is safe at any moment, including from a session going through the gateway. |
| **Everything is a module** | Gateway, breaker, UI, entry point, message catalog. Coupling is by capability, nothing imports its peers, and a distribution is a *module list* — not a fork. |
| **Zero dependencies** | No third-party packages, no `go.sum`, and the test suite passes with `GOPROXY=off`. Chinese width, message catalogs and terminal layout are hand-written. |

## The seam

The kernel is mechanism. Which modules ship, whose quirks get patched, in what
order — a distribution decides, from another repository:

```go
app.Main(ctx, app.Options{Loader: app.Selection{
    Disable: []string{"cli"},                                        // by directory name
    Extra:   []app.Entry{{Dir: "deepseek", Component: deepseek.New()}},
}})
```

The kernel contains the string `deepseek` nowhere outside a doc comment, and
"this module cannot be removed" is derived from the port the composition root
consumes — never from a name list.

## Quick start

```bash
make build            # → bin/newgate (the kernel's own modules; mechanism harness)
bin/newgate init && bin/newgate start
bin/newgate status    # who is going through newgate, and which profile
bin/newgate doctor    # when something is wrong
```

> The binary this repo builds is **not the product** — it has the kernel's
> modules and no upstream-quirk patches. Products come from a distribution:
> [**rzbdz/newgate-ext**](https://github.com/rzbdz/newgate-ext).

## Docs

| | |
| --- | --- |
| [docs/00-index.md](docs/00-index.md) | the map |
| [docs/03-architecture.md](docs/03-architecture.md) | ownership, layers, the boundary to distributions |
| [docs/13-i18n.md](docs/13-i18n.md) | how the interface speaks more than one language |
| [CLAUDE.md](CLAUDE.md) | the house rules a contributor (human or agent) follows |

## License

Pre-1.0 and still moving. Ask before you build something you intend to depend on.
