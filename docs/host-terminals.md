# Host terminals

Host terminals are operator shells that run in **tmux on the daemon host** — not
in a container. They exist for the founding use case Dejima grew out of:
non-conflicting, separately-instanced, resumable terminals (for navigating,
modifying, and repairing the server itself) without hand-rolling tmux + ssh.

## The invariant: agents are contained, humans are not

The line that makes this safe is **not** "everything in an island" — it's:

> **Agents are always contained. Humans are not (it's their box).**

A host terminal is a *human* tool: you, operating your own server, just
multiplexed and reachable from any device. It is **not** an agent. The word
"agent" always implies a container; "terminal" always implies a host session.
There is deliberately no such thing as a host *agent* — if you want broad
power for an agent, give a *contained* agent scoped authority (a Home Island
over the token path) or use a blank island. Keeping that wall is the point.

## Security model — the most privileged surface in the system

A host terminal is an **uncontained shell on the daemon host**, so it is treated
as the most sensitive thing Dejima exposes:

- **Off by default.** Start the daemon with `dejimad --host-terminals` (or
  `DEJIMAD_HOST_TERMINALS=1`) to enable it. The daemon logs a warning at startup
  when it's on.
- **Operator-only, never island-reachable.** The `/v1/terminals*` routes are
  absent from the token-auth allow-list (`internal/api/tokenauth.go`), so an
  in-island token is denied them by default — locked in by test. They live on
  the operator control plane only.
- **Audited.** Create, attach, detach, and delete are logged.
- **Not a promoted/public API.** The endpoints are an implementation detail of
  the TUI. There is intentionally no webhook/SDK/automation surface for driving
  host shells — minimizing the blast radius.

## Using them (TUI)

When the daemon has host terminals enabled, the TUI shows a **`Host · not
contained`** section below Islands:

- **`t`** — new host terminal + attach (skips all repo/agent ceremony).
- **`⏎`** on a terminal — attach / resume the live session.
- **`d`** — close it (kills the host tmux session; confirmed).
- The detail pane spells out *"a shell on the daemon host — NOT contained."*

Terminals persist (the tmux session survives disconnects and daemon restarts)
and are resumable from any device — the same attach transport as island agents.

## Model / where things live

- Each terminal is a tmux session on the host named `dejima-term-<id>` (ids
  `t1`, `t2`, …, with an optional label).
- The registry persists at `~/.dejima/host-terminals.json` (`internal/hostterm`);
  the tmux lifecycle and PTY bridge live in `internal/api` + `internal/bridge`
  (`HostPTY` / `AttachToHostTmux`, the no-container path).

## Platform note

The PTY/tmux path runs on whatever host the daemon runs on. tmux must be
installed on the daemon host. On macOS/Linux this is the same `tmux` the islands
use inside their containers — here it just runs on the host directly.

## Roadmap

A `dejima term` CLI (`ls` / `new` / `attach` / `rm`) is intentionally **not**
built yet — keeping host terminals TUI-only for now bounds the surface of the
most privileged feature. The endpoints support it whenever we want it.
