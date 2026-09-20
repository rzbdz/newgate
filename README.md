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
