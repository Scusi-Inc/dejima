# Island PID-1 unification — retire the "primary agent" entirely

**Status:** roadmapped, **target pre-1.0, NOT launch-blocking.** Prereq shipped: the
user-facing primary is already removed from the TUI/CLI flow ("Path A" — see
below). This doc covers "Path B": removing the *last* place the first agent is
special — the container entrypoint.

## Background: what "primary" is, and why it exists

`project.PrimaryAgent()` is just `Agents[0]`. It wears two unrelated hats:

1. **User-facing default** (mostly removed by Path A): the no-id attach target
   (`dejima connect <island>`, TUI Enter), the "can't remove the primary agent"
   guard, and creator wording.
2. **The container's PID 1** (still coupled): at provision the container
   entrypoint (`image/start.sh`) is configured from the first agent's *type*:
   - first agent **interactive** → PID 1 = `exec tail -f /dev/null` (a keepalive);
     the container outlives every agent. Agents are `docker exec` tmux sessions.
   - first agent **headless** → PID 1 = `exec /bin/sh -c "$DEJIMA_AGENT_CMD"`; the
     container *is* that command and dies when it exits.

"PID 1" = a container's first process; the container lives exactly as long as
PID 1 lives. Processes started later via `docker exec` are children and don't
keep it alive.

There are two launch paths, which is the whole asymmetry:
- the **first** agent is launched by the **entrypoint** at boot;
- **every other** agent is launched by the daemon via `ensureAgentSession` /
  `reconcileAgentsAsync` (`internal/api/server.go`) — a `docker exec tmux
  new-session` into the already-running container. The wake path even says so:
  `// the entrypoint relaunches the primary; restore the rest`.

## Path A (shipped/planned) — remove the user-facing primary

- `dejima connect <island>` (no id) → the in-island shell (matches TUI Enter +
  SSH, which already shell in via `docker exec`).
- Allow zero-agent **interactive** islands; remove the "can't remove the primary"
  guard for them. Agent order becomes purely cosmetic → unblocks agent reorder.
- Headless-first islands keep one wart: the first agent **is** PID 1, so it can't
  be freely removed (removing it = stop/purge the island). A guards this with a
  rule; the asymmetry survives. Internal `Agents[0]`/`p.Agent` plumbing stays.

## Path B (this doc) — make no agent ever special

Always use the keepalive entrypoint and launch **every** agent (including the
first, including headless) through the path that already exists for non-primary
agents. Then no agent is ever PID 1; deleting/reordering any agent is uniform;
zero-agent islands work for every island type; "primary" is gone for real.

**Why it's feasible (not a rewrite — deleting a special case):** the daemon
*already* runs any agent as a supervised, non-PID-1 process.
`ensureAgentSession` (server.go) launches **both interactive and headless**
agents in a tmux session via `docker exec` — headless ones redirect to a
per-agent log and self-respawn on crash (`agentLaunchScript`). This is used for
every non-primary agent today. B promotes it to the universal path.

**Concrete changes:**
1. `image/start.sh` → always `exec tail -f /dev/null` (drop the headless-PID-1
   branch and the "launch primary in tmux" branch; remove the
   `DEJIMA_LAUNCH`/`DEJIMA_AGENT_CMD` branching).
2. Provision (`server.go`): launch the **first** agent via `ensureAgentSession`
   like the rest (the `seedAgents[1:]` loop becomes all); stop baking the first
   agent into the entrypoint env.
3. Wake (`reconcileAgentsAsync`): restore **all** agents, not "the rest."
4. `dejima logs <island>` (no id): today a headless-first island's container
   stdout *is* the agent output; under B that moves to the per-agent log
   (`headlessLogPath`), so the no-id logs default must point there.
5. Retire/relax: the "can't remove the primary" guard (gone — keepalive
   survives), and `p.Agent`/`p.Cmd`'s role as the entrypoint command (becomes
   vestigial/display only; keep for back-compat or drop with the wire type).

**Risks / things to get right (why this wants live verification):**
- Headless-island **log routing** change (container stdout → per-agent log).
- Headless now supervised by the tmux restart-loop instead of Docker's PID-1
  lifecycle (the restart loop already exists, but it's a behavior change).
- Touches the island **lifecycle** (provision, wake, entrypoint, logs) — the
  riskiest paths; needs a real daemon to verify wake/restart + OOM defaults.

## Migration: switching active users A → B

No flag-day, no data loss. Handle it **lazily, on container recreate** (exactly
how image upgrades already work — state lives in the workspace/home volumes):

- **Interactive-first islands: migrate for free.** They already run `tail -f
  /dev/null` as PID 1; B's `reconcileAgentsAsync` is idempotent
  (`ensureAgentSession` skips an already-running session), so a B daemon managing
  an A interactive island is a no-op for its already-launched first agent.
- **Headless-first islands: must be recreated to flip.** Their running A
  container has the headless command as PID 1; if a B daemon also reconciled that
  agent it would **double-run** it (PID 1 + a tmux copy). So B must NOT reconcile
  the first agent of a legacy container.
- **Mechanism:** tag each island with an **entrypoint generation** (legacy-A vs
  keepalive-B). The daemon launches the first agent via reconcile only for
  keepalive-B islands. An A island flips to B on its next **recreate**
  (`upgrade`/`reset`), never on a plain `wake`/`start`. Existing islands keep
  working; they convert on the next recreate.

## Relationship to Path A

A and B are sequential: A removes the user-facing primary and ships the
dashboard/reorder wins at low risk; B removes the final structural coupling. A
leaves exactly one defensible wart (headless-first PID-1), which B dissolves.
Do A now; schedule B pre-1.0 once a host is available to verify the lifecycle
paths.
