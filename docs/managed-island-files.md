# Managed island files & version-skew detection

Two facts about an island's filesystem drive this design:

- **Daemon-owned ("managed") files are re-derived on every boot.** The hook
  scripts and their `settings.json` wiring are copied from the image's `/opt`
  tree and reconciled by the agent shim (`image/agents/claude-code/init.sh`) each
  time the container starts. They are NOT user data — never hand-edit them and
  expect the edit to stick.
- **User data is sticky.** The workspace volume (`/workspace`, the repo clone and
  the agent's work) and the per-island home volume (`/home/dejima`: tool auth,
  caches, the user's own `settings.json` keys) persist across restarts, recreates,
  and `dejima upgrade`.

The line between the two is what makes `dejima upgrade` safe AND effective: it
recreates the container (picking up a freshly built image + new managed shims)
without touching the user's work or credentials.

## Why managed files self-heal every boot

The motivating incident: an island built from a pre-2026-06-13 image still carried
the OLD unix-socket agent-event hook (and its `settings.json` wiring). On a
TCP-only daemon that hook silently `exit 0`'d, so the island's agent-state
**heartbeat never fired — for 18h, with no signal anywhere.** Everything that
reads the heartbeat went dark: mail-nudges, idle auto-hibernate, and the
`dejima_agent_idle_seconds` metric. The daemon and CLI were current; only the
island layer was stale.

The root cause had two halves:

1. The island's `/opt` copy of the hook script was stale (an old image).
2. `init.sh` wrote the `settings.json` hook block **only if absent** — so even
   after the script was refreshed, an island that already had a (stale) hook
   block kept the old wiring forever.

### The fix

`init.sh` now treats the hook script and its wiring as managed files,
re-derived every boot:

- **`dejima-notify.sh`** is copied from `/opt` unconditionally (already the case).
- **The `Notification`/`Stop` → `dejima-notify` wiring in `settings.json` is
  reconciled idempotently**, not "only if absent". Each boot it strips any prior
  `dejima-notify` entries and re-adds the current contract — a **merge**, so the
  user's own `settings.json` keys and their own Notification/Stop hooks are
  preserved; only the dejima-owned entries are refreshed. (If `jq` is missing or
  the file is malformed, it falls back to writing the minimal canonical wiring so
  the heartbeat still works.)

So a `dejima upgrade` against a freshly built image now self-heals the
socket→TCP class of break: the script is fresh and the wiring is reconciled.

## Propagating a fix: rebuild, then upgrade

`dejima upgrade <name>` recreates the container against the **current** local
image (`dejima/island:latest`); it does **not** rebuild that image. To propagate
a shim change you do two steps:

```
dejima image build          # rebuild dejima/island:latest with the new /opt shims
dejima upgrade <name>        # recreate the island against it (or --all)
```

If you skip `dejima image build`, the upgrade reuses the existing local image and
the managed files are re-derived from whatever that image holds — so the shim fix
won't land. `dejima doctor`'s `island image` line shows the locally built image's
id for confirmation.

## Detecting skew (so you know to upgrade)

Islands are **stamped** with the daemon version they were built and last upgraded
against (`built_version` / `upgraded_version` in `dejima.toml`). When an island's
stamp is behind the running daemon, it was built from an older image and may carry
stale managed files. This is surfaced with the exact remedy inline:

- `dejima doctor` — flags the island:
  `built on v0.1.4, daemon on v0.5.3 — stale island image … fix: dejima upgrade x`
- `dejima ls` — a compact `NOTE` column on flagged islands.
- `dejima status <name>` — a `built on:` line and a `skew:` remedy line.

A second, independent signal catches a broken hook even when versions match: the
**zero-heartbeat** flag. A running island that has emitted no agent-state event
since boot (past a short grace window) is flagged with the same
`dejima upgrade <name>` remedy — an upgrade re-derives the managed shims. This is
the signal that would have caught the motivating incident directly.

Out of scope (deferred): automatic host-update / `upgrade --all` propagation that
bulk-recreates islands. Detection + the inline remedy is the safe slice; the
operator decides when to roll.
