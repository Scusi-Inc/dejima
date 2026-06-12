# Dejima — The Port (brokered host-file access) — design & decision record

**Status:** Adopted — V1 built and **validated against a live Docker host** (`scripts/integration.sh` — 31/31 ALL GREEN on Minion, 2026-06-12). Phase 2 (Home Island) core built; brain-driven path and read-write deferred. Living record.
**Last updated:** 2026-06-12
**Extends:** `roadmap.md` §134 ("Local content ingestion for content-digesting agents") — advances it from *leaning (a)* to a decided (a)+(c) shape. Realizes the **Intake / Trade / Ledger** primitives named in `positioning.md` §"core tension" for the file domain.
**Constrained by:** `positioning.md` (containment-first; not the brain; the contributor decision rule), and `roadmap.md` §152 (no inter-island channel).

This document is the reviewable record for brokered host-file access for content-digesting and
assistant-class agents. It is a living document; phases update it as they ship.

> **Naming note.** "The Port" is the broker *role*; it is realized as a subsystem of the existing
> per-host daemon **`dejimad`**, not a separate container. There is exactly one `dejimad` per host,
> which is precisely the "one Port per host" property (§3.1). A **Home Island** *is* a real
> container (an island with `role=home`); the Port is not.

---

## 0. Status at a glance

| Capability | State |
|---|---|
| Scope registry (deny-all default), `dejima port grant/list/revoke` | **Built** |
| Hash-chained, tamper-evident Ledger; `dejima audit [--verify]` | **Built** |
| `Intake` (host→island), read-only, symlink-safe, fail-closed-ledgered; `dejima port intake` | **Built** |
| `Export` (island→host staging); `dejima port export` | **Built** |
| Home Island role + `dejima home create` + native-vs-island fork | **Built (core)** |
| Live-Docker end-to-end validation (`scripts/integration.sh`) | **Green** — 31/31 on Minion, 2026-06-12 |
| Brain-driven autonomous Port/spawn (macOS TCP path) | Deferred — §3.2 |
| SSH-façade adoption wedge + framework adapters | Phase 3 — §5 |
| Read-write trading (`:rw` grants + `dejima port write` into a scope) | **Built** — symlink-safe, fail-closed `trade.write` |
| Live brokered mount (FUSE/9p) | Phase 5 — §6 |

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

The decided answer is a **broker**: the host daemon `dejimad` is the only thing holding host-FS
access, and it mediates, scopes, and **ledgers** every file that crosses between the host and any
island. The historical Dejima *was* the single sanctioned port; ships traded *through* the
island, they did not sail into Nagasaki. Same here.

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
   HOST (Mac mini / VPS) │  dejimad  — the one host daemon = the Port    │
                         │  · holds the only host-FS scopes (vault, …)   │
   ~/Obsidian/Vault  ───►│  · per-island scope + policy (ro [/ rw later])│
   ~/Documents       ───►│  · writes every crossing to ~/.dejima/        │
                         │    ledger.jsonl (append-only, hash-chained)   │
                         └───────────────┬───────────────┬──────────────┘
                            intake/export (brokered)  intake/export (brokered)
                                         │                │
                  ┌──────────────────────▼──┐   ┌─────────▼──────────────────┐
                  │  HOME ISLAND (role=home) │   │  PROJECT ISLAND "myrepo"   │
                  │  assistant brain         │   │  coding agents (worktrees) │
                  │  (OpenClaw / Hermes …)   │   │                            │
                  │  headless, 24/7,         │   │  spawned by the brain via  │
                  │  persistent + hibernates │   │  the Dejima API, torn down │
                  └──────────────────────────┘   └────────────────────────────┘
                         A and B never see each other (roadmap §152 holds)
```

- **`dejimad` is the Port.** It is the *only* component with host-FS scopes. Project and home
  islands have **zero** direct host-FS handle.
- An island reaches host content **only** by trading with the Port: `Intake` (brokered in) and
  `Export` (brokered out to host-owned staging), scoped and policy-checked per island. Writing
  *into* a user scope (`trade.write`) is the read-write milestone (§6), not V1.
- Every crossing is an event on the Ledger. This is non-optional for any island with a Port
  scope (§4).

---

## 3. Architecture pillars

### 3.1 One Port per host (centralized broker, not a per-island sidecar)

The broker is a single per-host daemon — `dejimad` — not one sidecar per island.

- **Why centralized:** the Port is the *one* trust anchor that holds host-FS access. N sidecars
  = N privileged surfaces to harden and N copies of scope/policy to keep consistent; the whole
  value is that there is exactly one sanctioned point of contact. It also gives a single,
  coherent Ledger rather than N fragmented logs. `dejimad` is already one-per-host, so this falls
  out for free.
- **Scopes are per-island, host-side, and not island-writable.** A grant is a `PortScope`
  (`{name, host_path, mode, granted_at}`) stored in the island's config
  (`~/.dejima/projects/<name>/config.toml`, mode `0600`). The island has no handle to its own
  config, so it cannot widen its own grant. Default is **deny-all** — empty scope list means the
  island reaches no host content outside its repo.
- **Non-interaction is preserved structurally:** the Port keys every scope and every Ledger
  entry by island. Island A's scope is invisible to Island B even though both talk to the same
  daemon — the daemon refuses to bridge their scopes. Centralization does **not** mean shared
  state between islands.
- **Failure domain:** the Port (`dejimad`) is host-side and outside every container's blast
  radius (as is the Ledger). A compromised island cannot reach the Port's host-FS handles — it can
  only issue scoped, logged trade requests the Port may refuse.
- **Reachability:** clients (CLI, wrappers, an in-island brain) reach `dejimad` over its unix
  socket (`~/.dejima/dejimad.sock`) or an optional Tailscale-pinned TCP listener. See §3.2 for the
  macOS in-island caveat.

### 3.2 The Home Island (the brain runs *in* an island, not native on the host)

The default deployment maps an assistant orchestrator into its **own long-lived, persistent
island** — a *Home Island* (`role=home`) — rather than running it native on the host.
`dejima home create --cmd "<brain launch>" --repo <url>` provisions it: a headless island whose
command is the framework daemon (e.g. `openclaw gateway`), restart-on by default, marked with
`DEJIMA_HOME=1` so the brain can self-identify.

- **Why (prompt-injection):** an assistant reads untrusted inbound channels (WhatsApp, Signal,
  email, model output). That is the highest prompt-injection surface in the system. If the
  orchestrator runs native on the host, a single injection inherits `$HOME` instantly and
  **bypasses Dejima entirely** — you would have armored the hands (leaf coding tasks) and left the
  head bare. Containing the brain closes that gap: an injected brain can still only issue *scoped,
  ledgered* trade requests to the Port.
- **Why this is not the Letta antipattern:** Letta is walking away from spawning a *fresh
  container per ephemeral sub-task* — high churn, latency tax. A Home Island is the opposite: **one
  persistent island that hibernates and survives restarts** — Dejima's existing sweet spot. The
  orchestrator runs as a `headless` agent (the handler already exists; `multi-agent-spec.md`).
- **What the brain does:** it reaches user content through the Port (§3.1) and spawns **Project
  Islands** for risky code work via the public Dejima API, tearing them down when done. The brain
  is a client of the runtime, consistent with `roadmap.md` §134 option (a) — but it lives *inside*
  containment, and reaches host content via (c) the brokered channel, never (b) a raw host mount.
- **Reachability caveat (deferred work).** A *host-driven* trade — the user or a wrapper running
  `dejima port intake …` — works everywhere today, including macOS. A *brain-driven* trade — the
  orchestrator inside the island calling the Port on its own — needs an in-island handle to
  `dejimad`. That handle is the bind-mounted unix socket, which works on **Linux** hosts but **not
  on macOS** (Docker Desktop/colima cannot bind-mount a unix socket over virtiofs). On macOS the
  brain must reach `dejimad` over the **Tailscale TCP listener** instead. Wiring that
  authenticated in-island TCP path is deferred (it also couples to multi-agent); until then,
  brain-driven autonomy is a Linux-host capability and macOS uses the host-driven path.

### 3.3 The native-execution fork (the one sanctioned exception)

Some assistant capabilities are **host-OS-native and un-brokerable as files** — triggering a
macOS Shortcut, reading Apple Notes (an API, not a file), sending iMessage, AppleScripting a
native app. A container cannot reach these even through a perfect file broker.

The decision criterion is **surfaced at setup, not defaulted** — `dejima home create
--explain-native` prints it:

> **Does this agent need host-OS-native capabilities (Shortcuts / Notes / iMessage /
> native-app control), or only files + code execution?**
> - **Files + execution only → Home Island** (§3.2). Full security story.
> - **Needs host-OS-native capabilities → runs native**, and we state plainly that Dejima
>   contains the blast radius of its *file and code actions* (via the Port and Project Islands),
>   **not** the orchestrator itself.

This makes the residual risk an explicit, informed choice rather than a silent default. Brokering
the host-OS capabilities themselves (so the brain could stay contained *and* fire a Shortcut) is
the capability-broker question (§3.4) — deliberately out of scope.

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

Built as `internal/ledger`: an append-only, hash-chained log at `~/.dejima/ledger.jsonl`,
host-side, outside every container's blast radius.

- **One substrate, not a second log.** Brokered file operations are typed events on the one audit
  stream, queried through one `dejima audit`. Event types in use: `port.grant`, `port.revoke`,
  `trade.read` (intake), `trade.export` (export to staging). The read-write milestone (§6) adds
  `trade.write` (into a scope). (`positioning.md`: compose from primitives, don't fork them.)
- **Each entry records:** `seq`, `time`, `type`, `island`, `agent`, `scope`, `path` (within
  scope), `mode`, `bytes`, `sha256` (content hash of the bytes that crossed), `decision`
  (allowed/denied), `detail`, plus `prev` and `chain` — each entry's `chain` is
  `SHA-256(prev ‖ entry)`, so any in-place edit or deletion breaks the chain.
- **Tamper-evident, and verifiable from the CLI.** `dejima audit --verify` walks the chain and
  exits non-zero if any link is broken. (HMAC-keyed chains are an option per `roadmap.md` §117.)
  The live-Docker integration test proves this end-to-end: it corrupts a recorded field, asserts
  `--verify` fails, restores the bytes, and asserts it passes again.
- **Trade events are non-optional for any island with a Port scope**, even while the *general*
  API/lifecycle ledger stays opt-in. Intake is **fail-closed**: the crossing is ledgered *before*
  any byte enters the island, so a failed audit write means the file does not cross. The
  justification is symmetric: the **only** reason it is safe to hand a self-hosted assistant your
  vault is that every touch is scoped and logged. The Ledger is not compliance garnish here — it is
  the thing that *earns* the access. The pitch: *"You can give your Jarvis your vault because the
  Port logs every read, per-island, append-only, hash-chained — and refuses to move a byte it
  can't log."*

---

## 5. Adoption surface — the SSH wedge, not Docker emulation (Phase 3)

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
  **Not built, and not to be built** without revisiting this section (§10.6).

> **Forward note (figure out later).** The same island-as-SSH-endpoint is also the on-ramp for
> **agent-IDEs** — VS Code / Cursor / Zed / VSCodium opening an island as a remote-dev target
> (today via "Attach to Running Container" over a remote Docker context; via Remote-SSH once the
> façade ships). A later **native Dejima editor extension** could surface islands, agents, and
> `port` trades directly in the IDE. Tracked in `roadmap.md` (Port section).

---

## 6. V1 constraints — prove the security model before touching read-write

V1 is the **security-foundation** release, not the assistant-enablement release: it serves the
*coding/ingestion* profile and proves the plumbing; it does **not** yet enable the live-assistant
profile (which needs read-write and liveness). V1 is **built, with the safety properties
unit-tested and validated end-to-end on live Docker**, under exactly these constraints (§8):

1. **Copy-in / copy-out only** (`Intake` / `Export`), not a live mount. A scoped file is brokered
   into the island; changes are brokered back out to host-owned staging explicitly.
2. **Read-only.** `:rw` grants are **rejected at grant time** until the read-write milestone.
   Export never writes into a user scope — only into `~/.dejima/projects/<name>/exports/`. RW into
   a user's primary files is where the real risk lives, and it does not land until the Ledger and
   scope enforcement are hardened in the field.
3. **Mandatory, fail-closed Ledger** for every brokered crossing (§4). No silent, unlogged trades.

Two safety properties are enforced and tested:

- **Path containment.** `resolveWithinScope` resolves symlinks and rejects anything that escapes
  the scope root — both `../` traversal and symlink escapes return `"… escapes the scope"`. The
  integration test asserts both refusals *by error string* (not merely a non-zero exit) and
  additionally asserts the out-of-scope secret never landed inside the island.
- **Deny-all default.** Intake against an island with no matching scope is refused (`"… has no
  Port scope …"`), before and after `revoke`.

Staging beyond V1 (each its own decision, §10): **Phase 4** read-write trading (a `:rw` grant +
`trade.write` into the scope, explicit opt-in per scope) → **Phase 5** a live brokered mount
(FUSE / 9p / virtio backed by the broker, so the island sees a directory while the Port mediates
and logs every operation).

---

## 7. Implemented surface (CLI / API reference)

| Command | Does | Ledger event |
|---|---|---|
| `dejima port grant <island> <host-path>[:ro]` | Grant a read-only scope (host path validated on the daemon host) | `port.grant` |
| `dejima port list <island>` | List an island's scopes | — |
| `dejima port revoke <island> <scope-or-host-path>` | Drop a scope | `port.revoke` |
| `dejima port intake <island> <scope>:<rel> [dest]` | Copy a host file (within the scope) into the island, read-only, symlink-safe, fail-closed | `trade.read` |
| `dejima port export <island> <container-path>` | Copy a file out to host-owned `exports/` staging | `trade.export` |
| `dejima home create --cmd "…" --repo <url> [--explain-native]` | Provision a Home Island (headless brain, `role=home`) | — |
| `dejima audit [--verify] [-n N]` | Show the Ledger; `--verify` checks the hash chain (non-zero on tamper) | — |

API routes: `…/islands/{name}/port/scopes` (GET/POST/DELETE), `…/port/intake`, `…/port/export`
(POST), and `GET /v1/audit`. Default intake dest is `/home/dejima/intake/<scope>/<rel>` inside
the island (the agent-owned home; the container root isn't agent-writable).

---

## 8. Validation

`scripts/integration.sh` is the end-to-end dogfood against a **live Docker host** — **31/31 ALL
GREEN on Minion, 2026-06-12** (it caught and drove fixes for three real bugs the unit tests
missed: intake's default dest landing in a root-owned dir, the multi-agent clone-wait for
no-remote seeds, and the test seed needing a Docker-shared path). In a throwaway `$HOME` it starts
its own `dejimad`, builds the island image, and asserts, against real
containers:

- deny-all before any grant; grant; intake (top-level, nested, custom dest) with content verified
  *inside* the container;
- traversal guards — `../` and symlink escapes **refused with the `"escapes the scope"` error**,
  plus a positive check that the out-of-scope secret never crossed;
- export to staging with content verified;
- Ledger records `port.grant` / `trade.read` / `trade.export`; chain `--verify` passes;
- **tamper-evidence** — corrupt a recorded field → `--verify` fails → restore → passes;
- revoke → back to deny-all;
- create-time multi-agent seeding (`init --agent X --agent Y` → `a2` worktree reconciles).

Negative assertions match the specific error string (not a bare non-zero exit), so an unrelated
failure cannot pass for a security guard.

---

## 9. Relationship to existing decisions

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
  is the same idea one level up — a shared, brokered resource *across* islands governed by scope +
  Ledger instead of a flat shared volume.

---

## 10. Telemetry note (adjacent, not in scope here)

Because Dejima sits at the PTY/transport and broker layers, it can surface telemetry app-layer
frameworks cannot easily compute (per-island resource use, token/byte velocity, the
tamper-evident event stream). **Bubble up and alert; do not autonomously kill.** An over-eager
circuit breaker that terminates a 24/7 assistant on a heuristic *is* the silent-overnight-crash
failure the audience most fears — self-inflicted. Autonomous termination, if ever built, is
opt-in and conservative. (Strategy framing lives in the private strategy repo, not here.)

---

## 11. Open questions

1. **Read-write timeline (Phase 4).** What concrete hardening gates RW? Field-proven Ledger +
   scope enforcement — measured how? RW adds a `:rw` grant path (today rejected) and `trade.write`
   into the scope.
2. **Live-mount mechanism (Phase 5).** FUSE vs 9p vs virtio-fs; which preserves the per-operation
   audit hook with acceptable latency?
3. **Brain-driven trades on macOS.** Wire the authenticated in-island → `dejimad` TCP path (the
   unix socket is Linux-only). Couples to multi-agent; sequence accordingly (§3.2).
4. **Capability brokering — go / no-go.** Do we ever broker a curated allowlist of host commands
   (Shortcuts, CLIs), or hold the files-only line permanently? (§3.4)
5. **Where the Port runs when the host is not the compute.** On a cloud-VM deployment, what is
   "the host" whose files are brokered? Likely: a no-op unless a host-FS scope is explicitly
   configured.
6. **Docker-façade — confirmed no-go.** §5 rejects it; record any future reconsideration here
   rather than reopening silently.

*Resolved:* scope-configuration surface — decided as the imperative `dejima port grant/revoke`
verb writing to per-island config (deny-all default); shipped in V1.
