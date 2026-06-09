# Dejima — positioning

**Last updated:** 2026-06-09

This note records *what Dejima is and isn't*, and why. It exists so that future
decisions — features, roadmap, marketing copy, contributions — can be checked
against a settled thesis instead of relitigated each time. If a proposal
contradicts this note, that's a signal to either reject it or consciously change
the thesis (and update this file), not to quietly drift.

---

## The one-sentence thesis

**Dejima is the neutral, containment-first *runtime* for agent-native work: the
persistent layer where agents live, below a control plane / UI and above
compute.** It is plumbing, not a product, and not a brain.

```text
Control plane / UI        e.g. Dispatch, a Slack bot, a web client, the CLI/TUI
        │
        ▼
Dejima runtime            persistence · isolation · sessions · access · events
        │
        ▼
Compute                   Mac mini · VPS · cloud VM · laptop
```

The middle layer is the part every serious agent system eventually rebuilds.
Dejima's bet is that **agent persistence and context persistence should be
first-class infrastructure, separate from both the model and the UI.**

---

## What Dejima *is*

- **A persistent runtime.** Islands (containers) outlive client disconnects,
  laptop sleep, and network blips. tmux inside, brokered PTY out.
- **Containment-first.** Each island is isolated by the kernel: its own `$HOME`,
  git config, credentials, network. Islands cannot see each other. The namesake
  is a *quarantined* trading post — isolation is the identity, not a feature.
- **Neutral and headless.** The same HTTP/websocket API the CLI uses is the one
  anyone builds on. No UI is privileged. No agent is privileged (Claude Code and
  Codex bundled; others via custom image).
- **Real-time, not historical.** It surfaces rich live state (presence, stats,
  git, crash health, an event stream). History, aggregation, dashboards, and
  per-user/org rollups are for the layer above.
- **Deployment-agnostic.** Identical behavior on a Mac mini, a Linux box, or a
  cloud VM. "Home server" is one *entry point* and a distribution story — not
  the ceiling of the idea.

## What Dejima *isn't*

- **Not a memory / intelligence layer.** It does not accumulate understanding of
  you, synthesize your notes, or act as a chief-of-staff. That's where durable,
  compounding value lives — and it belongs **above** Dejima (in Dispatch or a
  dedicated memory service), because it wants the *opposite* of containment
  (ambient access to everything). See "the core tension" below.
- **Not a UI or an end-user product.** The TUI/CLI is a reference client, not the
  product surface. If it can be a thin client of the API, it stays thin.
- **Not an orchestrator / swarm engine.** Multi-agent *collaboration* and
  cross-island channels are deliberately out of scope (a context-bleed vector).
  Real value tends to come from 1–3 persistent, well-contextualized agents, not
  27 chatty ones. Orchestration is the wrapper's job, driving N islands via the
  API.
- **Not a model host.** No bundled LLM weights. The agents are the models, and
  they run inside islands. Any natural-language control reuses credentials the
  user already has.
- **Not an analytics / cost dashboard.** It exposes the signals; wrappers track
  them.
- **Not a cloud competitor.** It runs *on* AWS/GCP/Azure/a Mac mini — it doesn't
  compete with them.

---

## The core tension (and how we resolve it)

There is a real pull toward "your journal, notes, code, calendar, email all
available to one local agent" — a unified personal-context layer. That vision is
genuinely valuable **and architecturally opposed to Dejima's identity.** Dejima
isolates; a pan-context intelligence wants omniscience.

We resolve this deliberately, not by accident:

- Dejima stays containment-first and exposes **scoped, brokered persistence
  primitives** — durable volumes, agent state, an event stream, and the
  **Intake / Trade / Ledger** concepts (brokered in, brokered out, audited).
- A memory/context layer, if built, **composes those primitives from above
  without collapsing the silo.** It is a separate component (Dispatch or a
  sibling service), never a god-agent reaching through isolation.

The bridge between "neutral runtime" and "personal intelligence" is the brokered
event/state substrate — not a hole punched through containment.

---

## Value capture — eyes open

"The Docker of agent persistence" is a useful *internal lens* for how neutral and
composable the runtime should be. It is **not** a strategy claim, and the analogy
carries a warning: git captured no value (GitHub did); Docker-the-tech won while
Docker-the-company nearly died (registries and clouds above it captured the
value). **Ubiquitous infrastructure tends to be monetized one layer up.**

So the conscious stance:

- Treat the Dejima runtime as **strategic OSS infrastructure** that commoditizes
  the complement and makes the control plane (Dispatch) better.
- Expect **value capture to happen at the control-plane / memory layer**, not in
  the runtime.
- "Neutral standard everyone builds on" is a *different, harder* bet than
  "Dispatch's strategic backend." It requires external adopters and probably a
  spec — not just good code. Today the primary consumer is Dispatch. Don't
  confuse the two positions.

---

## Decision rule for contributors

Before adding anything to Dejima, ask:

> *Can a layer above the runtime compute this from already-surfaced primitives?*

If **yes**, it does not belong in Dejima — expose the primitive and let the
wrapper own it (memory, synthesis, dashboards, cost, orchestration, multi-tenant
UIs).

If **no** — it requires engine-level access, containment guarantees, or the
persistence/session substrate itself — it may belong here.

See [`roadmap.md`](roadmap.md) for how this rule has already shaped scope
(observability, auth-stops-at-roles, no-bundled-LLM, no inter-island comms).
