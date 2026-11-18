# Kernel docs index

This repository is the **kernel** of newgate: the component framework, the
process-entry contract, the i18n engine and the tooling. It ships no modules.
Anything that describes how the product behaves — the gateway, configuration and
roles, reasoning pass-back, client takeover, operations, troubleshooting — lives
in the distribution (`newgate-ext`, `docs/`), because that is where those modules
live.

| # | Document | Answers |
| --- | --- | --- |
| 2 | [02-component-framework.md](02-component-framework.md) | What concepts does the component kernel have? |
| 3 | [03-architecture.md](03-architecture.md) | What is the kernel/distribution boundary, and what runs assembly? |
| 9 | [09-extension-guide.md](09-extension-guide.md) | How do I add a component, an upstream patch, a shared-port mount, a process entry? |
| 10 | [10-testing-security.md](10-testing-security.md) | How is the kernel verified, and what are the trust boundaries? |
| 13 | [13-i18n.md](13-i18n.md) | How do user-visible strings get localized? |

Product documents (moved out on 2026-11-18): product overview, configuration,
gateway behaviour, reasoning, clients/runtime, operations, troubleshooting,
remote design — see the distribution's `docs/`.

## Current terms (kernel side)

| Term | Definition |
| --- | --- |
| Component | A lifecycle node in the capability graph |
| Capability | A typed port identity components use to reach each other |
| Provision / Requirement | Static declaration that a component provides / needs a port (`Need` hard, `Optional` weak) |
| Plan | A resolved graph: order fixed, no `Start` run |
| Selection | The table a composition root hands the kernel: which components to install |
| Entry | The process-entry contract (`component/entry`): who claims this invocation |
| Release | Undo handle for a runtime registration (`Register*` on an owner service) |
