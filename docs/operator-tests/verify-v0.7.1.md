# v0.7.1 wave — operator verification (2026-06-29)

Verified live on **Minion** (host) + **GIZMO** (Windows client) after cutting
v0.7.0 + v0.7.1. The wave: image-input, names-primary agent labels, usage
signals, key-health, mailbox persistence, governed sub-agents, egress gate.

## Release
- **v0.7.0 @ `99001ba`** (pre-wave baseline) + **v0.7.1 @ `master`** (the wave). Both
  pipelines green: release binaries (6 targets) + npm + Homebrew tap + SDK (PyPI/npm).
  Verified live: GitHub release `v0.7.1=Latest`; `curl` installer resolves to v0.7.1;
  `npm dejima@0.7.1`; brew tap bumped.

## Scorecard

| Feature | Status | How verified |
|---|---|---|
| **Image-input** | ✅ PASS | drag-drop **+** Ctrl-V **+** Alt-V each delivered a `0644` agent-readable PNG to `~/intake/paste/`; agent `Read` all three. Current UX = **bare-path → Read**. |
| **Names-primary** | ✅ PASS | TUI shows **label-first** (role names, not IDs); `added agent kid (v2)` confirms `label (id)` + unique-ID + label auto-increment (`kid`→`kid-2`). |
| **Usage signals** | ✅ PASS | resource-vs-cap + key-health render in the TUI; **token/cost populated** (`44.7k`) after a real prompt on a v0.7.1 island; graceful `n/a` (not a fake `0`) when nothing reported. |
| **Key-health** | ✅ PASS | `no model key` flag on `janus/openclaw`. |
| **Mailbox persistence** | ✅ PASS | 28-message history (incl. pre-restart messages) survived `dejima service restart --system`. |
| **Sub-agents (governance)** | ✅ PASS | live from a real island token: **deny-without-grant**, **must-be-ephemeral**, **co-located** (`/workspace/.agents/<id>`), **agent-can-spawn-but-not-reap** all enforced. |
| **Egress gate** | ⬜ not verified | deferred to a follow-up / agent check. |

**6.5 / 7 personally verified** (sub-agent *governance* confirmed; *visibility/UX* follow-ups below).

## Findings → v0.7.2 follow-ups (docketed)
1. **Host terminal (`/`) crash loop** — host tmux spawned with **no `$TERM`** (dejimad under launchd has none) → `terminal does not support clear` → instant exit → **infinite, un-cancellable reconnect**. Fix: set `TERM` on `HostPTY` + make a permanent open-failure fail once/cancellably. Mitigation today: `/tui default`. *(d5)*
2. **Image auto-ingestion** — current UX is bare-path→`Read`; make paste/drop auto-ingest as an image attachment + clean up `~/intake/paste/`. *(a1)*
3. **In-island spawn entry point** — no `dejima` CLI in a fresh island, and `dejima agent add` lacks `--ephemeral` → an agent can't invoke spawn without raw API. *(a1)*
4. **Spawn lineage invisible** — `spawned_by` + `ephemeral` come back `null` in the agent list (smells like the `created_at` omitempty-on-save family). Blocks the TUI render below. *(a1)*
5. **Agent self-reap** — an agent can spawn but not delete its own ephemeral children (operator-only today). Add scoped self-reap: delete allowed iff `ephemeral` **and** `spawned_by == caller` **and** same island. *(a1)*
6. **TUI sub-agent rendering** — indent agent-spawned sub-agents under their spawner, grey/italic, ephemeral marker (needs #4 first); **+ a per-island spawn-grant control** (grants are CLI-only today). *(a2)*
7. **Rendering hazard** — flicker-free rendering over the lossy bridge → constant Ctrl-L + a phantom `/clear` (input desync/replay on reconnect). Reconnect must not replay stale input. *(d5)*

## Clean-Mac gate (separate track)
- **curl channel GREEN (21/21)** on a real Mac = the v0.7.0 install verdict. The brew/npm reds were `/opt/homebrew` permission errors (test environment, **not** product).
- ⚠️ The **full (brew+npm) gate is THROWAWAY-BOX ONLY — never a live-daemon host.** Run co-resident with a live daemon it took Minion's daemon **offline** on 2026-06-29 (recovered, no data lost). Blocked on the co-residency guard. See **Operator action items** in `docs/roadmap.md`.

## Environment notes
- Existing islands run on **stale pre-v0.7.1 images** until `dejima upgrade <name>`; the wave's in-island features (e.g. the usage-reporting shim) need an upgraded or freshly-created island. The `dejima ls` "stale image / no heartbeat" notes are the canary.
- The operator login auto-attaches to a dejima session; for a clean **host** shell use `ssh -t aoos@minion tmux new-session -A -s NEW`.
