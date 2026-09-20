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

## What you get

**Takes over your CLI, without your CLI knowing.**
A PATH shim and the client's own config rewritten in place — env injected
silently, byte-exact backups taken first. Install once; every switch after that
happens at the gateway, so you never reopen a session.

**Changing which API answers is one command, not a migration.**
`newgate profile ark` — the next request takes the new chain. Your client keeps
running, its config is untouched, no session is interrupted, and the old one
stays one command away.

**Nothing dies with a single upstream.**
Per-tier candidate chains, ordered by your rules and then by predicted
time-to-first-byte. Every skip is explained, every reroute is logged, and the
response tells you where it went.

**A breaker that knows whose fault it was.**
Availability, rate-limit and config failures are separate ledgers with separate
thresholds. A 400 caused by the *shape of your request* is counted against
nobody. Half-open trials recover on their own.

**Metrics you can act on.**
Latency by context size, failovers, first-byte timeouts, rewrites,
cancellations — each counter with a one-line explanation, because a number
nobody can interpret is decoration.

**Restart without dropping a request.**
The listening socket is handed to the new process and the old one drains what's
in flight, streams included. Upgrading is safe in the middle of an agent session.

**English today, 中文 today, anything tomorrow.**
Every user-facing string comes from a message catalog, and the source-language
path is the identity — English output is byte-identical to a build without i18n.
`newgate lang zh-Hans`, or let it follow the system.

**Everything is a module — including the parts you'd expect to be special.**
Gateway, breaker, UI, entry point, message catalog. Coupling is by capability,
nothing imports its peers, and a distribution is a *module list*, not a fork.

**Zero dependencies.**
No third-party packages, no `go.sum`, and the test suite passes with
`GOPROXY=off`. CJK width, message catalogs and terminal layout are hand-written.

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
"cannot be removed" is derived from the port the composition root consumes —
never from a name list.

## Quick start

```bash
make build            # → bin/newgate (kernel modules only: a mechanism harness)
bin/newgate init && bin/newgate start
bin/newgate doctor    # when something is wrong
```

> That binary is **not the product** — no upstream-quirk patches. Products come
> from a distribution: [**rzbdz/newgate-ext**](https://github.com/rzbdz/newgate-ext).

## Docs

[docs/00-index.md](docs/00-index.md) — the map ·
[docs/03-architecture.md](docs/03-architecture.md) — ownership and the boundary
to distributions · [CLAUDE.md](CLAUDE.md) — the house rules a contributor
(human or agent) follows.

## License

Pre-1.0 and still moving. Ask before you build something you intend to depend on.
