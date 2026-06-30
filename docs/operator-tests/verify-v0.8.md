# Operator verify pass — v0.8 (consolidated)

Everything shipped in the **v0.8 wave** (v0.7.1 → v0.8.0) that can't be exercised
from a build island (no GUI/tmux/Docker/host there). Run on **Minion** in a GUI
terminal. This supersedes `verify-v0.7.1.md` and the stale `v0.6.*` verify docs.
Most items are a quick eyeball.

When done, **ping a2** with results (`dejima msg send --to a2 "<results>"` from an
island, or just tell the owner) — a2 relays "verified" and closes the loop with
a1/d5 on anything that fails.

```bash
dejima update            # → v0.8.0
# then RESTART dejimad on Minion so daemon-side defaults take effect:
dejima service restart --system      # (host-terminals on-by-default, egress-on, etc. are DAEMON changes)
dejima version           # confirm 0.8.0 on BOTH client and daemon
```

> Existing islands run on **stale pre-v0.8 images** until `dejima upgrade <name>`.
> In-island features (the baked `dejima` CLI, `--ephemeral`, the spawn-reporting
> shim) need an **upgraded or freshly-created** island. The `dejima ls`
> "stale image / no heartbeat" note is the canary.

---

## A. Team-in-the-TUI — invite → paste → connect  (the v0.8 headline; dogfood with a real teammate)
The no-CLI path to add a teammate. **This is the one to dogfood with Amanda.**

Operator side (on Minion):
- [ ] `I` opens the **Team** overlay (owner-only; a non-owner credential shows an "owner-only" panel, not an empty form).
- [ ] Invite: pick **role** (operator/viewer — owner is *not* offered), **scope** (all islands or a checkbox subset), optional **label**, set **Host** to the tailnet address the teammate dials (e.g. `minion.ts.net:7274`; prefilled when you're already remote), **Create**.
- [ ] The minted panel shows a **one-paste `dejima-invite:…` blob** (shown once) + the `dejima join` hint.
- [ ] Issued tokens list; `d` revokes one.

Teammate side (a second machine / account — Amanda's brew-installed client):
- [ ] **First run, no daemon:** `dejima` asks one routing question — *"set up Dejima on this machine, or join one that already exists?"* → `j` → paste the invite → "Joined … as <role>" → dashboard. **No env vars.**
- [ ] **Already in the TUI:** Connection (`s`) → `J` → paste the invite → connected.
- [ ] Unrecognized key at the routing question opens the dashboard (does **not** install a colliding daemon).
- [ ] CLI twins also work: `dejima token invite --role … --host …` / `dejima join <blob>`.

## B. Host-terminal band (v0.8 fixes)
- [ ] Footer shows **`[/] terminals`** (host terminals are on by default after the daemon restart).
- [ ] `/` **opens** the band; `/` again **closes** it (toggle — the regression that left you stuck is fixed).
- [ ] Expanded band header shows the actions: **`⏎ open · d delete · [/] collapse`**.
- [ ] `d` / `Del` / `Backspace` on a terminal → confirm → it's removed.
- [ ] `⏎` on a terminal opens it in a **new window/tab** (does not hijack the dashboard); a freshly-created one likewise.
- [ ] **No `/` crash loop:** opening a host terminal no longer instantly exits / loops on `terminal does not support clear` (host PTY now gets a usable `TERM`). A genuinely unrecoverable open fails **once**, cancellably — never an infinite reconnect.

## C. Sub-agents — rendering + budget (needs a real spawn; see §D to create one)
- [ ] An agent-spawned sub-agent renders **indented under its spawner**, **dim/italic**, with a **`· ephemeral`** marker — visibly distinct from a top-level agent.
- [ ] Island action menu (`m`) → **"Sub-agent budget…"** opens the spawn-grant control: shows granted/used; set **max concurrent / max total / per-agent TTL / per-agent memory**; `⏎` applies.
- [ ] Setting **max concurrent → "off"** (or `x`) **revokes** the grant; a revoked island's agents can't spawn (deny-all default).

## D. Sub-agent ergonomics (a1 — in-island)  *(needs an upgraded/fresh island)*
- [ ] A fresh island has the **`dejima` CLI on PATH** (run `dejima` inside the island shell — no "command not found").
- [ ] `dejima agent add --ephemeral …` from inside an island spawns a sub-agent **only within the operator's grant** (deny without a grant; must be ephemeral).
- [ ] An agent can **self-reap** its own ephemeral child (`dejima agent rm`), but **not** a sibling/parent or a non-ephemeral agent.

## E. Egress (v0.8 default change)
- [ ] A new island's egress proxy is **on by default**; allow/deny changes apply **without** an island restart.

## F. Session exit / reconnect
- [ ] Detaching a session (Ctrl-b d) or the terminal window closing **exits cleanly** — no spurious reconnect trap. A real link drop still auto-reconnects.

---

## Standing operator tasks (not a quick eyeball — owner/host only)
See **`docs/roadmap.md` → Operator action items** for the full list. The live ones:
- [ ] **Inter-island live-verify** — `operator-tests/inter-island-wave.md` (deny-all → grant → cross-island message → action approve/deny → **wake-on-message**).
- [ ] **Phase-B nightly dispatch** — `workflow_dispatch` the nightly on the `macos-mini` runner (`run_system_tests=true`; `run_reboot_test=true` for reboot survival). See `lanes/lane-6-phase-b.md`.
- [ ] **Runner boot-persistence** — auto-login for `dejimaqa` + `svc.sh install` (physical access).
- [ ] **Watchtower stand-up** — `dejima home create --name watchtower …` (headless; see `drift-checker-design.md`).
- [ ] **Full clean-Mac gate (brew+npm)** — **throwaway box only, NEVER Minion.** Blocked until the co-residency guard lands (in progress — a2). Until then: do not run it on a live-daemon host.
