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

## Quoting multi-line text — read this before your first commit

When passing prose to `git commit -m`, `dejima msg send`, or any command that
takes a message body, use a **quoted heredoc**:

    git commit -F - <<'EOF'
    subject line

    Body with `backticks` and $(parens) that stay literal.
    EOF

Never a double-quoted string. Inside double quotes the shell runs anything in
backticks or `$(...)` and splices the output into your text. `git merge` takes
`-F <file>`, not `-F -`, so write the message to a file for that one.

This has bitten five times this week across four agents, on `git commit -m` and
`dejima msg send`. Commit messages and agent messages have shipped with silent
holes where a code reference used to be — and once, with the quoted command
actually executed.

It keeps happening because **it fails toward looking fine**. Nothing errors; you
get plausible text with a gap in it, and the author is the one person who can't
see the gap, because they know what they meant and read it back in. Assume you
will not notice. Use the heredoc.

Be useful. Be specific. Commit as you go; don't accumulate large uncommitted
changes.

## Where the knowledge is

`docs/README.md` maps all 88 docs by the question you arrived with. Two are
worth reading before you diagnose anything or write a guard:
`docs/testing/guards-need-controls.md` (does this check have a SUBJECT?) and
`docs/testing/readings-go-stale.md` (is this reading CURRENT?).

**A lesson that recurs twice becomes a check, not a third comment.** On
2026-09-03 five pieces of knowledge failed to propagate; every one was already
documented, correctly, and three were in the SAME FILE as the code that got them
wrong — one of them twenty lines above the edit. Proximity is not the fix,
because the reader is not failing to find the rule, they have a specific correct
belief that makes it feel inapplicable. If you are about to write a fourth
paragraph about something that keeps happening, write a gate instead;
`scripts/` has five and they work.
