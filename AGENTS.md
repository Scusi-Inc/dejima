# Environment: Dejima island

You are running inside a Dejima island — a containerized workspace dedicated to
this single project. Some things worth knowing:

- The repo is checked out at `/workspace`. That's the working tree.
- You can `git push` directly — credentials for GitHub are set up via `gh`.
  The `gh` CLI is available for `gh pr create`, `gh issue view`, etc.
- The container has no access to the host filesystem, other Dejima projects,
  or the host's shell aliases. If you need a tool, install it; it stays in
  this island.
- Disconnects from the user are normal: you may notice the user reconnects
  from a different device. The session persists; pick up where you left off.
- Anything you write under `/workspace` persists across container restarts.
  Anything outside `/workspace` (e.g., installed packages) is also persisted
  for the lifetime of this island but will be discarded when the user runs
  `dejima purge`.

Be useful. Be specific. Commit as you go; don't accumulate large uncommitted
changes.
