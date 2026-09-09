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

## Read `CLAUDE.md` — it is not Claude-specific

The working rules for this repo live in the root `CLAUDE.md`, and despite the
name none of them are about Claude. They are deliberately kept in one file rather
than copied here, because two copies of a rule drift and the stale copy still
reads as authoritative. Read it before your first commit. It covers:

- **Staying in your own worktree.** Branch refs are shared across worktrees, so
  `git checkout -B master` in your own worktree can stage deletions of another
  agent's just-landed files. Use `git switch -c <mybranch> origin/master`, and
  run `git worktree list` before your first edit.
- **The Go toolchain**, which is hand-installed and lost on every island rebuild.
  Wrong arch or wrong version fabricates segfaults that read like a corrupt
  checkout.
- **Quoting multi-line text**, for commit messages, `gh` bodies and
  `dejima msg send`. The failure is silent and produces plausible text.
- **Detaching heavy commands**, because the operator is attached to your pane.
- **How the site publishes**, which is from the root of `master` and not from
  your branch.

Two more worth reading before you diagnose anything: `docs/README.md` maps the
docs by the question you arrived with, and `docs/testing/` holds the
failure-shape library.

Be useful. Be specific. Commit as you go; don't accumulate large uncommitted
changes.
