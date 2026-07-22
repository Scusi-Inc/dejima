# Automated test harness — design

## Goal

End-to-end automated coverage of the **CLI, TUI, API, daemon, and live islands** — to
(a) catch regressions on every PR, and (b) **replace most of the manual Minion
verification pass** that currently gates each release. The manual "Operator verification
queue" should shrink to "things genuinely needing a human's eyes," not "everything."

## What's automatable (and what isn't)

- **Automatable:** CLI (flags, exit codes, output), API (HTTP/WS), **TUI** (bubbletea via
  `teatest` — feed key events, assert rendered frames), daemon logic, real island
  lifecycle (Docker), Port / MCP / audit / inter-island flows, and the macOS-host bits
  (`service install`, `onboard`, Keychain, idle-hibernate) on real hardware.
- **Smoke-only (nondeterministic):** real-LLM agent behavior — assert "it ran / produced
  a commit," never exact output.
- **Inherently manual:** nothing structural — interactive prompts are driven via the API /
  non-interactive flags, which Dejima mostly supports.

## Tiers

| Tier | What | Runner | Cadence |
|---|---|---|---|
| **1** | unit + API (`httptest`) + CLI-vs-fake-daemon + **TUI (`teatest`)** | GitHub-hosted ubuntu | **every PR** |
| **2** | real islands via real CLI+API+containers: lifecycle, Port, MCP, audit, **inter-island link/action/wake** | a Docker box (small Linux cloud VM, or the Mac mini) | nightly / `live` label |
| **3** | macOS-host: `service install`/launchd, `onboard --provision-host`, Keychain, idle-hibernate, terminal reconnect, **per-adapter wake**, SSH-façade | **Mac mini** self-hosted runner (dedicated test user) | nightly |
| **4** | real-agent smoke: launch an agent, trivial task, push to a test repo, exercise inter-island | Mac mini / Docker box | nightly (lenient) |

Gating: Tiers 2–4 run on a **nightly schedule + `workflow_dispatch` + a `live` PR label** —
**never every PR** (cost + hardware wear).

## Orchestration

- Build on `scripts/integration.sh` (the existing Docker suite) — factor into reusable
  steps; a harness script runs the matrix and emits a clear pass/fail report + a
  `make test-integration` target.
- **TUI:** `teatest` models per view (drive the bubbletea model directly — no daemon).
- **Isolation/teardown:** a disposable island namespace per run; purge after; revoke
  tokens; never touch real projects. The dedicated macOS test user keeps it off your main
  environment.

## Operator setup (what you create — needed for Phases B–C)

- **A dedicated macOS test user** on the Mac mini; register a **GitHub Actions self-hosted
  runner** as that user (label e.g. `macos-mini`).
- **A throwaway GitHub account + a test repo + a fine-scoped PAT** (repo scope on the test
  repo only) for clone/push tests.
- **A throwaway agent credential** (a cheap/throwaway Claude API key or test account) for
  the Tier-4 agent-launch smoke.
- *Optional:* a **test Tailscale auth key** (or we loopback that path).
- *Optional but recommended:* a **small Linux Docker box** (~$5/mo) for Tier 2, so the Mac
  mini is reserved for the genuinely-macOS Tier 3.
- **Repo secrets to add** for the live workflows: `TEST_GH_TOKEN`, `TEST_AGENT_KEY`,
  (`TEST_TS_AUTHKEY`).

## Phasing

- **Phase A — no accounts needed; start now.** Tier-1 `teatest` TUI tests + CLI/API
  coverage, and expand `integration.sh` into the full Tier-2 matrix (runnable on any
  Docker host). Catches regressions on every PR. Brief: `docs/lanes/lane-6-test-harness.md`.
- **Phase B — needs the macOS test user + GitHub account.** Register the self-hosted
  runners, add the Tier-3 macOS suite, wire the nightly workflow.
- **Phase C — needs the agent credential.** The Tier-4 real-agent smoke (kept lenient).

Each phase: small PRs, `go build`/`vet`/`golangci-lint`/`go test` green; the
`integration.sh` expansion is shellcheck-clean and run live on a Docker host (the build
island has none).
