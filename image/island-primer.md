# Your environment: a Dejima island

You are an AI agent running INSIDE a Dejima island — a Docker container dedicated
to a single project (`<island>`), on the operator's own hardware. A few things
that are easy to get wrong:

**Your filesystem is the container's, not the host's.** `/workspace` is this
project's git worktree; `/home/dejima` is your home. These are a SEPARATE
namespace from the operator's machine — your paths are NOT host paths, and there
is NO shared filesystem with the host. Do not try to reach host files, and do not
loosen permissions (e.g. `chmod` your home) to "bridge" to the host — it won't
work and isn't how files move here. (The one sanctioned way to touch host files
is brokered + audited via Port, and only if the operator grants it — never the
raw filesystem.)

**Moving files across the boundary.** Files arrive via the intake drop at
`/home/dejima/intake/`; the operator can also copy files in/out from the host
with `dejima cp` (daemon-brokered). You don't need to — and can't — reach across
the boundary yourself. Never chmod or symlink to try.

**Reaching the daemon.** You talk to the Dejima daemon over `DEJIMA_HOST`
(token-scoped, already in your env). Your island token is intentionally limited —
it CANNOT create islands, reach other islands, or touch the control plane. That's
containment, by design. You CAN coordinate with other agents in THIS island via
`dejima msg`, and (only within an operator-granted budget) spawn ephemeral
sub-agents.

**Sessions & git.** Disconnects/reconnects are normal — the operator may attach
from different devices; your session persists, so pick up where you left off.
`git push` works (credentials are set up). Commit as you go; don't accumulate
large uncommitted changes.

**You may not be alone.** An island can hold several agents, each in its own git
worktree, sharing the repo + credentials. Run `dejima msg poll` to see who else
is here.
