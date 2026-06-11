# Dejima — The Port Island (brokered host-file access) — design & decision record

**Status:** Draft — approved direction, pre-implementation
**Last updated:** 2026-06-11
**Extends:** `roadmap.md` §134 ("Local content ingestion for content-digesting agents") — advances it from *leaning (a)* to a decided (a)+(c) shape. Realizes the **Intake / Trade / Ledger** primitives named in `positioning.md` §"core tension" for the file domain.
**Constrained by:** `positioning.md` (containment-first; not the brain; the contributor decision rule), and `roadmap.md` §152 (no inter-island channel).

This document captures the decisions made while planning brokered host-file access for
content-digesting and assistant-class agents. It is the reviewable record before code lands.
It is a living document; phases update it as they ship.

---

## 1. Why

Today an island sees its repo (`/workspace`), its own `$HOME`, read-only host credential
mounts, and nothing else of the host (`v1-spec.md`; `multi-agent-spec.md` §6). That is exactly
right for a **coding agent**: a git checkout is the whole job.

A second audience has arrived. **Assistant-class agents** — OpenClaw, Hermes, Letta and the
"self-hosted AI operator" profile generally — exist to digest *local content that is not a git
repo*: an Obsidian vault, a Notes export, a Downloads folder, a documents tree. A 24/7
assistant that can only see one checkout is useless. This is the pressure `roadmap.md` §134
names.

The naive answer — a raw host bind-mount (`dejima init --mount ~/Vault:/data/vault:ro`) —
**is rejected.** It punches a live host-FS handle into the container, makes any `:rw` mount of
`$HOME` credential-equivalent (root over the user's data), gives **no enforcement or audit
point** (the kernel does the mount; nothing observes it), and **breaks island non-interaction**
the moment two islands mount overlapping paths — they now share state through the host FS,
defeating the property the per-island network isolation works to guarantee. It is "a hole
punched through containment," which `positioning.md` forbids by name.

The decided answer is a **broker**: a single hardened host-side daemon — the **Port Island** —
that is the only thing holding host-FS access, and mediates, scopes, and **ledgers** every file
that crosses between the host and any island. The historical Dejima *was* the single sanctioned
port; ships traded *through* the island, they did not sail into Nagasaki. Same here.

### What this is *not* (read before §2)

- **Not an inter-island channel.** The Port brokers **host ↔ island**, never **island ↔
  island**. Island A and Island B each trade *independently* with the host broker and remain
  blind to one another. This does **not** reopen the cross-island "trade primitive" rejected in
  `roadmap.md` §152 — there is still no sanctioned path between two islands.
- **Not a capability broker.** The Port brokers **files**, not **host commands, OS APIs, or
  arbitrary side effects.** See §3.4.
- **Not the brain.** The assistant orchestrator is a *tenant* that runs inside an island and a
  *client* of the broker. Dejima hosts it; Dejima does not become it (`positioning.md`).

---

## 2. The model

```
                         ┌──────────────────────────────────────────────┐
   HOST (Mac mini / VPS) │  Port Island  — ONE broker daemon per host    │
                         │  · holds the only host-FS scopes (vault, etc.)│
   ~/Obsidian/Vault  ───►│  · per-island scope + policy (ro/rw)          │
   ~/Documents       ───►│  · writes every crossing to the Ledger        │
                         └───────────────┬───────────────┬──────────────┘
                                 trade (brokered)   trade (brokered)
                                         │                │
                  ┌──────────────────────▼──┐   ┌─────────▼──────────────────┐
                  │  HOME ISLAND             │   │  PROJECT ISLAND "myrepo"   │
                  │  assistant brain         │   │  coding agents (worktrees) │
                  │  (OpenClaw / Hermes …)   │   │                            │
                  │  headless agent, 24/7    │   │  spawned by the brain via  │
                  │  persistent + hibernates │   │  the Dejima API, torn down │
                  └──────────────────────────┘   └────────────────────────────┘
                         A and B never see each other (roadmap §152 holds)
```

- The **Port Island** is a privileged, hardened host-side daemon. It is the *only* component
  with host-FS scopes. Project and home islands have **zero** direct host-FS handle.
- An island reaches host content **only** by trading with the Port: `Intake` (brokered in) and
  `Trade` (brokered out), scoped and policy-checked per island.
- Every crossing is an event on the audit stream (the **Ledger**). This is non-optional for any
  island with a Port scope (§4).

---

## 3. Architecture pillars

### 3.1 One Port per host (centralized broker, not a per-island sidecar)

A **single** broker daemon per host, not one sidecar per project island.

- **Why centralized:** the Port is the *one* trust anchor that holds host-FS access. N sidecars
  = N privileged surfaces to harden and N copies of scope/policy to keep consistent; the whole
  value is that there is exactly one sanctioned point of contact. It also gives a single,
  coherent Ledger rather than N fragmented logs.
- **Non-interaction is preserved structurally:** the Port keys every scope and every Ledger
  entry by island. Island A's scope is invisible to Island B even though both talk to the same
  daemon — the daemon refuses to bridge their scopes. Centralization does **not** mean shared
  state between islands.
- **Failure domain:** the Port is host-side and outside every container's blast radius (like the
  Ledger). A compromised island cannot reach the Port's host-FS handles — it can only issue
  scoped, logged trade requests the Port may refuse.

### 3.2 The Home Island (the brain runs *in* an island, not native on the host)

The default deployment maps an assistant orchestrator into its **own long-lived, persistent
island** — a *Home Island* — rather than running it native on the host.

- **Why:** an assistant reads untrusted inbound channels (WhatsApp, Signal, email, model
  output). That is the highest prompt-injection surface in the system. If the orchestrator runs
  native on the host, a single injection inherits `$HOME` instantly and **bypasses Dejima
  entirely** — you would have armored the hands (leaf coding tasks) and left the head bare.
  Containing the brain closes that gap: an injected brain can still only issue *scoped, ledgered*
  trade requests to the Port.
- **Why this is not the Letta antipattern:** Letta is walking away from spawning a *fresh
  container per ephemeral sub-task* — high churn, latency tax. A Home Island is the opposite: **one
  persistent island that hibernates and survives restarts** — Dejima's existing sweet spot. The
  orchestrator runs as a `headless` agent (the handler already exists; `multi-agent-spec.md`).
- The Home Island reaches user content through the Port (§3.1) and spawns **Project Islands** for
  risky code work via the public Dejima API, tearing them down when done. The brain is a client of
  the runtime, consistent with `roadmap.md` §134 option (a) — but it lives *inside* containment, and
  reaches host content via (c) the brokered channel, never (b) a raw host mount.

### 3.3 The native-execution fork (the one sanctioned exception)

Some assistant capabilities are **host-OS-native and un-brokerable as files** — triggering a
macOS Shortcut, reading Apple Notes (an API, not a file), sending iMessage, AppleScripting a
native app. A container cannot reach these even through a perfect file broker.

The decision criterion, to be surfaced at setup (not defaulted):

> **Does this agent need host-OS-native capabilities (Shortcuts / Notes / iMessage /
> native-app control), or only files + code execution?**
> - **Files + execution only → Home Island** (§3.2). Full security story.
> - **Needs host-OS-native capabilities → runs native**, and we state plainly that Dejima
>   contains the blast radius of its *file and code actions* (via the Port and Project Islands),
>   **not** the orchestrator itself.

This makes the residual risk an explicit, informed choice rather than a silent default.

### 3.4 Files, not capabilities (the scope boundary)

The Port brokers **files** — byte flows in and out, under per-island scope and policy. It does
**not** broker host commands, OS APIs, or arbitrary side effects.

- **Why the line is here:** a file ledger is tractable — path, direction, island, agent, bytes,
  hash, timestamp. A *capability* ledger ("the agent ran some host command with some effect") is
  not — you would be auditing arbitrary side effects, and the Ledger's value (§4) collapses.
- Capability brokering (a curated allowlist of host commands the Port will run on an island's
  behalf) is a deliberately **deferred, much larger** surface. It is named here so it cannot
  sneak into the file broker. See §10.

---

## 4. The Ledger — mandatory for the broker, unified with the audit stream

`roadmap.md` already plans an opt-in `Ledger` (§80) and a broader audit ledger (§117). The Port
sharpens both:

- **One substrate, not a second log.** Brokered file operations are *typed events on the one
  audit stream* — `trade.read`, `trade.write`, `intake.*` alongside `api.request`,
  `lifecycle.*` — queryable through one `dejima audit`. (`positioning.md`: compose from
  primitives, don't fork them.)
- **Trade events are non-optional for any island with a Port scope**, even while the *general*
  API/lifecycle ledger stays opt-in. The justification is symmetric: the **only** reason it is
  safe to hand a self-hosted assistant your vault is that every touch is scoped and logged. The
  Ledger is not compliance garnish here — it is the thing that *earns* the access. The pitch:
  *"You can give your Jarvis your vault because the Port logs every read and write, per-island,
  append-only, hash-chained."*
- **Tamper-evident.** Entries are append-only and hash-chained (HMAC option per `roadmap.md`
  §117), and the Ledger lives host-side, outside every container — a compromised island cannot
  rewrite its own history.
- **Each entry records:** island, agent id, scope, direction, path (within scope), bytes,
  content hash, policy decision (allowed/denied), timestamp.

---

## 5. Adoption surface — the SSH wedge, not Docker emulation

Assistant frameworks already abstract their execution runtime (Hermes ships **six** backends:
local, Docker, SSH, Singularity, Modal, Daytona; Goose has extension points). We meet them
*there*, without asking maintainers to write a `dejima://` integration.

- **Ship: an SSH-target façade.** Presenting an island as an SSH endpoint is a small, faithful
  surface natively supported by almost every orchestration framework. Cheap to build, hard to
  break, honest about semantics.
- **Ship: thin, upstreamed backend adapters** for the 1–3 frameworks that already pluginize
  their runtime. A seventh Hermes backend is a small PR *we* author — distribution without
  begging for buy-in, and it builds OSS goodwill.
- **Reject: a Docker-daemon façade.** Emulating the Docker Engine API is a tarpit (large surface,
  partial compatibility = subtle breakage = worse than real Docker), it **semantically lies**
  (frameworks expect ephemeral `--rm` containers; islands are deliberately persistent and
  hibernate), and — fatally — it **reintroduces the bind-mount hole** §1 rejects: a tool that
  thinks it's talking to Docker will pass `-v ~/Downloads:/data` and expect a raw mount. We would
  have to either honor it (lose containment) or silently rewrite it (lie about its semantics).
  Not built.

---

## 6. V1 constraints — prove the security model before touching read-write

V1 is the **security-foundation** release, not the assistant-enablement release. Be honest about
that in the roadmap: V1 serves the *coding/ingestion* profile and proves the plumbing; it does
**not** yet enable the live-assistant profile (which needs read-write and liveness).

V1 is strictly:

1. **Copy-in / copy-out only** (`Intake` / `Trade`), not a live mount. A scoped snapshot is
   brokered into the island and changes are brokered back out explicitly.
2. **Read-only by default.** Read-write is a separate, later milestone — RW into a user's primary
   files is where the real risk lives, and it should not land until the Ledger and scope
   enforcement are hardened in the field.
3. **Mandatory Ledger** for every brokered crossing (§4). No silent, unlogged trades.

Staging beyond V1 (each its own decision, see §10): read-write trading → live brokered mount
(FUSE / 9p / virtio backed by the broker, so the island sees a directory while the Port mediates
and logs every operation).

---

## 7. Phases (rollout)

| Phase | Status | Goal |
|-------|--------|------|
| **0** | Built | Scope registry on `Project` (deny-all default), hash-chained `internal/ledger`, `dejima port grant/list/revoke`. |
| **1** | Built | `Intake` (host→island) + `Export` (island→host staging), **read-only**, symlink-safe, fail-closed-ledgered. `dejima port intake/export`. |
| **2** | Partial | `Role`=home on `Project`, `DEJIMA_HOME` env, `dejima home create` + native-vs-island fork. **Deferred:** in-island brain-orchestration helpers and the macOS TCP-auth path for brain-driven Port/spawn (couples to multi-agent; surfaced as a caveat, not shipped). |
| **3** | Not started | SSH-target façade + first upstreamed backend adapter (Hermes or Goose). |
| **4** | Not started | Read-write trading (hardened scope + Ledger; explicit opt-in per scope). |
| **5** | Not started | Live brokered mount (FUSE/9p) — only after RW copy mode is proven. |

Capability brokering (§3.4) and the native-execution tooling (§3.3) are **not** on this ladder;
they are tracked as open questions (§10).

---

## 8. Relationship to existing decisions

- **`positioning.md` decision rule** ("can a layer above compute this from surfaced
  primitives?"): host-FS brokering **cannot** be computed above the runtime — it requires
  engine-level access and a containment guarantee. It belongs in Dejima. The *assistant* that
  consumes it does not, and stays a tenant/client.
- **`roadmap.md` §134** (local content ingestion): this spec is its resolution — (a) the wrapper
  (Home Island) drives, reaching content via (c) a brokered channel; (b) raw host-content mount
  stays rejected.
- **`roadmap.md` §152** (no inter-island channel): untouched. The Port is host↔island only (§1,
  §3.1).
- **`multi-agent-spec.md` §6** (shared `/home/dejima` across agents *within* an island): the Port
  is the same idea one level up — a shared, brokered resource *across* islands governed by scope
  + Ledger instead of a flat shared volume. The abstractions should be checked for alignment
  before multi-agent Phase 2+ lands.

---

## 9. Telemetry note (adjacent, not in scope here)

Because Dejima sits at the PTY/transport and broker layers, it can surface telemetry app-layer
frameworks cannot easily compute (per-island resource use, token/byte velocity, the
tamper-evident event stream). **Bubble up and alert; do not autonomously kill.** An over-eager
circuit breaker that terminates a 24/7 assistant on a heuristic *is* the silent-overnight-crash
failure the audience most fears — self-inflicted. Autonomous termination, if ever built, is
opt-in and conservative. (Tracked in the strategy doc, not here.)

---

## 10. Open questions

1. **Read-write timeline.** What concrete hardening gates RW (Phase 4)? Field-proven Ledger +
   scope enforcement — measured how?
2. **Live-mount mechanism.** FUSE vs 9p vs virtio-fs for Phase 5; which preserves the
   per-operation audit hook with acceptable latency?
3. **Capability brokering — go / no-go.** Do we ever broker a curated allowlist of host commands
   (Shortcuts, CLIs), or hold the files-only line permanently? (§3.4)
4. **Scope configuration surface.** Where and how does a user declare a Port scope for an island —
   project config, a `dejima port` verb, both? What is the consent UX for granting a scope?
5. **Where the Port runs when the host is not the compute.** On a cloud-VM deployment, what is
   "the host" whose files are brokered? Likely: the Port is a no-op / disabled unless a host-FS
   scope is explicitly configured.
6. **Docker-façade — confirmed no-go?** §5 rejects it; record any future reconsideration here
   rather than reopening silently.
