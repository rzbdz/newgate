<div align="center">

# newgate

### A local semantic gateway for AI coding CLIs — **the kernel**

Your CLI asks for a *capability tier*. newgate decides the provider, the model,
and what to do when it fails.

`heavy` · `normal` · `mid` · `light` · `vision`

[![CI](https://github.com/rzbdz/newgate/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)
![build](https://img.shields.io/badge/build-fully_offline-blue)

**This repository is the kernel.** Mechanism only. Which modules ship, whose
quirks get patched, in what order — those are a *distribution's* decisions, and
they live in [newgate-ext](https://github.com/rzbdz/newgate-ext).

</div>

---

## The idea

Model routing is not a per-project decision, it is a per-*tier* decision. You
say "this needs the cheap fast one", not "this needs `gemini-2.5-flash-002` on
the second provider in my fallback list".

```text
┌──────────────┐   heavy / normal / mid / light / vision   ┌───────────────────┐
│  Claude Code │ ────────────────────────────────────────▶ │                   │
│  OpenCode    │                                           │      newgate      │
│  anything    │ ◀──────────────────────────────────────── │  (localhost:8899) │
└──────────────┘        one dialect, one endpoint          └─────────┬─────────┘
                                                                    │
                    profile → candidate chain → provider/model ─────┘
                    (fallback, first-byte timeout, quirks, cache)
```

A **profile** binds each tier to an ordered candidate chain. Switch profiles and
the next request uses the new chain — no client config is touched, no process is
restarted.

| tier | meaning |
| --- | --- |
| `heavy` | hardest reasoning, most expensive |
| `normal` | everyday driver |
| `mid` | the cost/capability compromise |
| `light` | fast and cheap (background calls, summaries) |
| `vision` | orthogonal image capability |

## Takeover is invisible, and that is the feature

You never point your CLI at newgate. `newgate on` points it for you, and the
client is not asked:

- a **PATH shim** sits in front of `claude` / `opencode`, so the binary you type
  is newgate wearing that client's name — `argv[0]` decides which client is
  being invoked, and one process serves all of them;
- the client's **own config is rewritten in place** — `settings.json`, env
  blocks, `opencode.json` — its model names replaced by tier names;
- **byte-exact backups** are taken before anything is touched, and `newgate off`
  restores those bytes, not "equivalent settings". The end-to-end suite checks
  the restore with a checksum.

The client never finds out. It reads its own config, believes it is talking to
Anthropic or DeepSeek, and the requests simply arrive here first: model resolved
from the tier, upstream quirks patched, request shape repaired, reasoning
handed back the way that upstream wants it. Sessions in flight are not
interrupted — a restart hands the listening socket to the new process and the
old one drains.

That is the whole trick, and it is why nothing has to be a plugin inside
anyone's editor: **takeover is a file swap and a shim**, and every client gets
it the same way. Per-client knowledge lives in a module (`modules/claudecode`,
`modules/opencode` in a distribution), including the part that only matters at
the crossing of one client and one model.

## Switching APIs costs one command

```bash
newgate profile glm     # the next request takes glm's chain
newgate tier normal     # and here is exactly where a `normal` request would go
```

Nothing else moves: your client keeps running, its config is untouched, no
session is interrupted. A profile is a name bound to per-tier candidate chains,
and `/p/<profile>/…` overrides it for a single request, so two upstreams can be
compared without touching global state.

When a provider goes slow, trips a breaker, or starts rejecting the shape of
your request, the chain moves on by itself in the order you wrote down. The
response says where it went (`X-Newgate-Route`), the log says why, and
`newgate tier <tier>` explains the whole chain — including every candidate that
was skipped and the reason. Nothing is silent: a rewritten request always
reports what was rewritten.

## Upgrades do not drop requests

`newgate restart` is not stop-then-start. The old process hands its **listening
socket** to the new one, then drains the requests it still has in flight (up to
ten minutes) while the new process serves everything new. Streaming responses
finish on the old process; the client never notices.

This is what makes swapping the binary safe at any moment — including from a
session that is itself going through the gateway, which is how this project is
developed: the agent writing the code is talking through the daemon it is about
to replace, in one command, with the old pid still serving the stream it is
reading.

## Everything is a module

There is no privileged core to extend. A component is a directory with a
`New()` that returns a `Component` — a name, a type, what it needs, what it
provides, and `Start`/`Stop`. Everything else in this repository is one:

| | |
| --- | --- |
| the gateway, the breaker, config, runtime, the CLI | modules |
| the entry ledger (`modules/entry`) | a module |
| the message catalog's owner (`modules/locale`) | a module |
| what a distribution adds | modules, from another repository |
| this repository's own binary | the composition root plus the kernel's modules |

Coupling is by **capability**, never by import: a module declares
`Need(gateway)` or `Optional(cli)` and gets a typed value, and no module
package is ever imported by the root or by its peers. `newgate plugin` lists
what is actually in this graph; `app/matrix_test.go` removes each module in
turn and asserts the graph either still assembles or fails naming the port that
went missing.

Two consequences worth stating, because they are the point:

- **The kernel does not know a single product module by name.** It contains the
  string `deepseek` nowhere outside a doc comment. Which modules ship, whose
  quirks get patched, in what order — a distribution decides, in a JSON file,
  in another repository.
- **"Cannot be removed" is derived, not listed.** Exactly one module resists
  removal — the entry ledger — and the reason is `app/manifest.go` reading the
  port the composition root itself consumes. Change what the root needs and the
  set changes with it. Nobody maintains a name list.

The user-visible proof is that `newgate` is itself one of those modules: the
entry point is claimed, not hard-coded, so a distribution can replace it, and
the skeleton distribution (framework + one `hello`) prints `hello world`.

## The one rule

> **Would this still belong here if someone shipped a completely different
> distribution?**

| yes → this repo | no → the distribution |
| --- | --- |
| gateway, breaker, takeover, UI, config, the component framework | upstream-quirk patches (DeepSeek's tail shape, GLM's reasoning hand-back) |
| client onboarding (Claude Code / OpenCode) | client × model cross semantics |
| **mechanisms** every distribution needs | **trade-offs** that only mean something for one upstream or one product |

The kernel does not know a single product module by name — it does not contain
the string `deepseek` outside of a doc comment. What a distribution gets is a
seam, and the seam is three types wide:

```go
app.Main(ctx, app.Options{
	Version: version, BuildTime: buildTime, CommitTime: commitTime,
	Loader: manifest.Loader{Spec: spec},   // ← whatever the distribution decided
})
```

A distribution supplies `app.Selection` — *disable these kernel modules, add
these of mine* — and gets back a process. `Selection.AllCore` (`"disable":
["*"]`) means "none of the kernel's modules": the skeleton distribution is the
framework plus one `hello` module, and `newgate` prints `hello world`.

One module can never be disabled: **`modules/entry`**, the entry ledger that
answers "who claims this `argv[0]`". That is not an exception carved out by a
name list — it is the composition root's own dependency, and
`app/manifest.go` derives it from the port the root itself consumes.

## What's inside

| module | what it owns |
| --- | --- |
| `component` | the typed capability graph and lifecycle kernel |
| `app` | the composition root: manifest, graph construction, `Main` |
| `modules/entry` | the entry ledger — who claims this invocation (never removable) |
| `modules/gateway` | the HTTP data plane and extension execution |
| `modules/breaker` | binding health: availability + latency ordering, as a policy plugged into the gateway's decision points |
| `modules/config` | configuration semantics and persistence (domain, resolver, store, dynamic roles) |
| `modules/confighook` | the runtime catalog of *which clients newgate can take over* |
| `modules/runtime` | daemon, process launch, PATH shim, takeover |
| `modules/cli` | the local control plane: dispatch, rendering, exit codes |
| `modules/locale` | which language this process speaks, and the `newgate lang` command |
| `modules/thinking` | model-agnostic thinking-mode policy |
| `modules/pluginmanager` | the runtime on/off ledger and module vocabulary |
| `modules/wrapper` | the policy behind PATH-shim takeover |
| `lib` | stateless shared helpers |

Every module directory has a root `module.go` and exports
`func New() component.Component`. Capability contracts live in that module's
root `api.go`, never in an `api/` subpackage — `app/layout_test.go` enforces
both, `app/direction_test.go` keeps the composition root from importing any
concrete module, and `app/independence_test.go` keeps distribution machinery
out of the kernel.

## Quick start

The kernel builds and tests itself with **no network and no second repository**:

```bash
make build          # → bin/newgate — the composition root + all 11 kernel modules
make check          # generate-check + gofmt + vet + tests + zero-token e2e
make static         # fully static binary for the host platform (verified with ldd)
```

Drive it:

```bash
bin/newgate init
bin/newgate start
bin/newgate status
newgate tier normal     # which chain would a `normal` request take?
newgate probe           # liveness / latency of every binding
newgate doctor          # the "why is this broken" command
```

> The binary this repo builds is **not the product**. It is a gateway with the
> kernel's modules and no upstream-quirk patches — good enough to develop and
> test the mechanism against, a downgrade in production. Products come from a
> distribution.

### Tests

```bash
GOPROXY=off go test ./...   # unit + system, offline by construction
go test -race ./...
make e2e                    # real binary + a byte-exact fake upstream: zero tokens
```

`mock/fake_upstream.py` replays real upstream behaviour byte for byte; the
distribution's end-to-end scripts reuse that same file rather than copying it,
because a copy drifts and a drifted copy is a green test that proves nothing.

## Language

The CLI speaks English (the source language) and follows your system locale:

```
NEWGATE_LANG  >  ~/.config/newgate/state.json  >  LC_ALL / LC_MESSAGES / LANG  >  English
```

Configurable settings beat the system locale on purpose: plenty of people run an
English box and still want to read Chinese. To pin it:

```bash
newgate lang            # what is in effect, where it came from, coverage per language
newgate lang zh-Hans    # persist; says so loudly if this shell's LANG overrides it
NEWGATE_LANG=zh-Hans newgate status   # one-off override
```

The kernel carries English plus Simplified Chinese. Translations live in
`lib/i18n/catalogs/` — a flat JSON map from the English source sentence to its
translation, with `machine`/`reviewed` marks on every entry. No message IDs, no
third-party i18n library; `make check-i18n` is the ratchet and
`docs/13-i18n.md` is the rationale.

## Layout

```text
component/     capability graph + lifecycle
app/           composition root (manifest, Main, ratchets)
lib/           stateless helpers
modules/       the 11 kernel modules — `ls modules/` and the table above agree
cmd/newgate/   the kernel's own binary (mechanism harness, not a product)
testing/       in-process harness for graph/system tests
mock/          fake upstream + zero-token end-to-end
tools/         the manifest generator (modules → app/modules_gen.go)
docs/          the design record — start at docs/00-index.md
```

## Reading order

| doc | why |
| --- | --- |
| [docs/00-index.md](docs/00-index.md) | the map |
| [docs/03-architecture.md](docs/03-architecture.md) | ownership, layers, and the boundary to distributions |
| [docs/09-extension-guide.md](docs/09-extension-guide.md) §8 | how a distribution consumes this repo |
| [CLAUDE.md](CLAUDE.md) | the house rules a contributor (human or agent) is expected to follow |

## Distributions

The flagship distribution — the one that wires this gateway to Claude Code and
OpenCode, with the upstream quirks patched — is
[**rzbdz/newgate-ext**](https://github.com/rzbdz/newgate-ext). Fork it, edit
`dist.json`, drop in your own modules, and you have your own.

## License

No license file yet — this is pre-1.0 and still moving. Ask before you build
something you intend to depend on.
