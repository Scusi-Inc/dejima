# Design: daemon scheduled-wake (implementation design for #118.2)

Design-first per a3 (#118/#123). Builds on d6's spec
([`scheduled-wake-spec.md`](scheduled-wake-spec.md)) — the *why* and the CLI shape
— and fills in the daemon-side design + the one part that makes it actually
useful: **on a scheduled wake the agent must run its task, not cold-start into an
empty container** (d6 #121). That couples scheduled-wake with the resume-on-wake
seam (roadmap: "Resume the agent session on wake"). **No code until a3 signs off.**

## The problem, precisely

Today an ambient agent has two bad options: stay resident (wasting a container +
RAM through long idle windows) or lean on a host launchd/systemd timer (off-thesis,
per-host toil). The interim `dejima pin` (#244) keeps it resident. The durable fix
is **hibernate-between-runs**: the always-on *daemon* holds a durable schedule and
wakes the island on cadence; the island does its work and hibernates again.

The catch d6 surfaced: waking the container is necessary but **not sufficient**.
The watchtower is a claude-code agent that must *run its skill* on wake. A bare
container start relaunches the agent process (via the entrypoint) but nothing tells
it to do the work — so we'd wake into an idle prompt and immediately re-hibernate,
having done nothing. So the primitive is really **wake + resume + run-task**.

## Existing seams we compose (no new subsystems)

| Seam | Where | Role in this design |
|---|---|---|
| Durable per-island config | `project.Project` in `~/.dijima/.../config.toml` (survives restart AND `dejima upgrade` — it's outside the container) | store the schedule |
| Idle tick loop | `RunIdleHibernator` / `scanIdle` (`internal/api/idle.go`) | add a sibling "due schedule" sweep on the same cadence |
| Wake path | `wakeIslandFor` / `wakeIsland` (`internal/api/{wake,server}.go`) → `StartContainer` + `reconcileAgentsAsync` | reuse verbatim to bring the island up |
| Wake event | `events.TypeIslandWoken` (already emitted on wake) | add `reason: "scheduled"` to the payload + ledger it |
| Task trigger | `injectFn` = `tmuxInject` (`internal/api/wake.go`, used by mail-nudges) | inject the scheduled task into the agent's session |
| Agent relaunch-on-wake | `reconcileAgentsAsync` + the container entrypoint | where the resume-on-wake flag (roadmap #28) plugs in |

## Proposed design

### 1. Schedule model (durable, daemon-owned)

Add to `project.Project` (mirrors how `NoHibernate` #244 landed — a config field,
not a new store):

```
Schedules []WakeSchedule `toml:"schedules,omitempty"`

type WakeSchedule struct {
    ID      string        // stable id for `schedule rm`
    Every   time.Duration // recurring cadence; 0 for one-shot
    At      time.Time     // one-shot absolute time (UTC); or the NEXT due time for recurring
    Task    string        // optional prompt/command to run on wake (see §3); "" = just wake
    Agent   string        // which agent runs Task (label/id); "" = primary
    NextDue time.Time     // computed; the tick loop compares against now
    LastRun time.Time     // for observability + catch-up dedupe
}
```

Config lives outside the container, so the schedule survives daemon restart **and**
`dejima upgrade` (the whole point — unlike an in-island cron that dies on recreate).

### 2. The tick: a sibling sweep on the idle loop

Reuse the existing cadence (`RunIdleHibernator`'s ticker, `min(5m, threshold/4)`,
floor 1m — minute granularity is plenty per d6). Add `scanSchedules(now)` alongside
`scanIdle`:

```
for each project p, for each schedule s:
    if now >= s.NextDue:
        wake(p)                     // reuse wakeIslandFor — no-op if already running
        runTask(p, s)               // §3
        emit island.woken{reason:scheduled, schedule:s.ID}   // + ledger
        advance(s)                  // recurring: NextDue = now.Truncate + Every; one-shot: delete
        p.Save()
```

**Catch-up, not stack** (d6): if the daemon was down across several due times, a
recurring schedule fires **once** on the next tick and re-anchors `NextDue` forward
(we never replay the backlog). One-shots that came due while down fire once, then
delete.

### 3. Run-task-on-wake — the compose point (the crux)

Waking isn't enough; the agent must *do the work*. Two composable mechanisms,
chosen per schedule:

- **(a) Injected task** — after wake, wait for the agent's tmux session to be ready
  (reuse the readiness probe `ensureAgentSession` / the agent-state heartbeat), then
  `injectFn(p, agent, s.Task)` — exactly the mail-nudge inject path. For the
  watchtower: `s.Task = "run the drift-check skill"`. This is the general answer and
  needs no adapter changes.
- **(b) Resume-on-wake (roadmap #28)** — for a *terminal* agent that should reattach
  its prior conversation, the relaunch uses the adapter's resume flag (Claude Code
  `--continue`/`--resume`, Codex equivalent) so a fresh tmux still has context.
  Scheduled-wake **composes with** this: the daemon relaunches with resume, *then*
  injects the task. If resume-on-wake isn't built yet, (a) alone still delivers the
  watchtower (a stateless skill run needs no prior context) — so **scheduled-wake
  can ship before resume-on-wake**, and gets richer when #28 lands.

**Readiness ordering matters**: inject must wait until the agent is actually at a
prompt, or the task is typed into a dead terminal. Gate the inject on the same
"fresh heartbeat / idle-at-boundary" signal the wake-nudge flush already uses
(`agentIdleAtBoundary`), with a bounded timeout → on timeout, ledger a
`scheduled-wake task not delivered` warning rather than silently dropping it.

### 4. Hibernate-again

Not this primitive's job to force. Two clean paths, both already exist:
- idle auto-hibernate reclaims the island after the task finishes and it goes idle
  (the symmetric pairing d6 describes), or
- the agent/skill hibernates itself when done (`dejima hibernate` from inside).

The schedule just guarantees the *wake*; what runs and when it sleeps is the
agent's business (d6's non-goal boundary).

### 5. CLI + API surface

Per d6's spec, addressed via the existing island routes (no new subsystem):
- `dejima wake <island> --at <RFC3339>` (one-shot), `--every <dur>` (recurring),
  `--task "<prompt>"` (optional), `--agent <label>` (optional).
- `dejima schedule list <island>` / `dejima schedule rm <island> <id>`.
- API: `POST/GET/DELETE /v1/islands/{name}/schedules` (operator-only —
  `capOperate`, absent from `tokenRouteAccess` so island tokens are denied, like
  resources/egress). **3 new routes → openapi.yaml + route-parity + SDK updates**
  (unlike #244, this one does add surface).

## Open questions for a3 (the design-review decisions)

1. **`--every` format**: a Go `duration` (`720h`) is simplest and matches d6's
   sketch; a cron expression (`0 3 * * 1`) is more expressive (wall-clock "3am
   Mondays") but pulls in a cron lib + parsing surface. Cadences here are
   hours/days — I lean **duration**, with cron as a later add if asked. Ruling?
2. **Ship order vs resume-on-wake (#28)**: I propose scheduled-wake ships with the
   **injected-task** mechanism (§3a) first — it fully unblocks the watchtower — and
   composes with resume-on-wake when #28 lands. Or do you want #28 designed/built in
   the same change so terminal agents resume context from day one?
3. **Where `Task` executes**: inject-into-tmux (§3a, general, no adapter change) vs a
   per-adapter "run this on wake" entrypoint hook (cleaner for headless, but adapter
   work). I lean inject-first, adapter-hook as a follow-up.
4. **One-shot GC**: delete a fired one-shot immediately (proposed) vs keep it with a
   `done` marker for `schedule list` history. Lean delete + rely on the ledger for
   history.
5. **Route surface**: dedicated `/schedules` sub-resource (proposed) vs folding into
   the island PATCH like #244. I lean dedicated — schedules are a list with their own
   create/list/delete, awkward as a PATCH field.

## Acceptance (from d6, made concrete)

- `dejima wake foo --every 720h --task "run drift-check"` on a hibernated island →
  wakes ~monthly with no host timer/resident process, **across daemon restarts and
  `dejima upgrade`**; on each wake the agent runs the skill; idle-hibernate returns
  it.
- `dejima schedule list foo` shows it; `schedule rm` cancels it.
- Each scheduled wake is in the audit/event stream (`island.woken` reason=scheduled),
  and a not-delivered task is warned, never silently dropped.

## Non-goals (unchanged from d6)

Not a general job scheduler; minute granularity; catch-up-not-stack; what runs on
wake is the agent's business.
