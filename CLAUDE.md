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

## You may be sharing this island — stay in your own worktree

Several agents can share an island, each with its own git worktree under
`/workspace/.agents/<id>`, while `/workspace` is a separate checkout another
agent actively commits to. Run `git worktree list` before your first edit.

**Branch refs are shared across worktrees.** Never `git checkout master` or
`git checkout -B master` in your worktree — use `git switch -c <mybranch>
origin/master`, which needs no shared ref. Git does not refuse the shared
checkout: on 2026-08-17 a `-B master` left one worktree's HEAD following the ref
while its FILES stayed at the old commit, and a routine `git add -A` staged
deletions of another agent's just-landed files. Read `git status` before every
commit and treat unexpected `D` lines as a stop signal, not noise.

## The Go toolchain is hand-installed, and getting it wrong fabricates crashes

`image/Dockerfile` installs no Go, so it is installed by hand into
`/usr/local/go` and every island rebuild loses it. After installing, check two
things:

- **`go env GOHOSTARCH` must match `uname -m`.** The container is aarch64. The
  default download is amd64 and installs fine — after which every `go` command
  runs under `qemu-x86_64` and, under memory pressure, segfaults at a DIFFERENT
  site each run. It reads exactly like a corrupt checkout. On 2026-08-31 this
  nearly cost a `git clean` on a worktree holding unpushed work.
- **Match `go.mod`, not latest.** `go.mod` says `go 1.26.3` and `ci.yml` pins the
  lint job to `1.26.x`. Go 1.27 makes golangci-lint v2.12.2 fail with
  `could not import math/rand/v2`; the linter cannot parse the newer stdlib, and
  the linter is the gate.

Correct install: `go1.26.3.linux-arm64.tar.gz` into `/usr/local`. `golangci-lint`
lives at `$HOME/go/bin` and survives rebuilds.

If `go` crashes oddly, check the toolchain before you suspect your own diff.

## The operator is attached to your pane

Your tmux pane is their screen, so anything a subprocess writes to stdout or
`/dev/tty` reaches them regardless of how you filter the tool result. Piping
through `grep` changes only what YOU see. `go test` also reprints a package's
buffered stdout when that package FAILS, so the wall arrives exactly when
someone is reading a failure.

Run tests, lint and builds detached, and read a line or two back out of the log:

    setsid --wait go test ./... </dev/null >"$SCRATCH/t.log" 2>&1

`setsid` also removes the controlling terminal, so a stray `/dev/tty` writer gets
ENXIO instead of their screen. When the complaint is "I keep seeing X", fix where
X is WRITTEN, not how you read it.

## The site publishes from master

dejima.tech is GitHub Pages served from the **root of `master`** (CNAME +
`.nojekyll` at repo root). Site edits on an agent branch publish nothing, however
green they are. Push site work with `git push origin HEAD:master` (rebase onto
`origin/master` first). The remote will report `Bypassed rule violations for
refs/heads/master: 3 of 3 required status checks are expected` — the push
succeeds and **CI never runs on it**. Say so in the handoff every time: a green
run list that silently excludes a commit is the failure mode this repo keeps
hitting. Verify by curling the live URL rather than trusting the push, and grep
for a string that sits on ONE line — `fit.txt` and other plain-text files wrap,
so a phrase spanning a line break returns zero from a page that is correct.

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

### One level deeper: `gh` bodies go through a FILE, never a heredoc

The rule above is necessary and not sufficient. `gh` needs a login shell here
(`GH_TOKEN` only reaches one), so the call is wrapped in `bash -lc '...'` — and a
heredoc nested inside those single quotes does not survive. The outer quoting
closes early, the body is re-parsed, and backticks and `$(...)` in your prose
execute. Observed on `gh pr create --body-file - <<'EOF'`: the PR was created
with a mangled body and the shell reported `command not found` for words out of
the markdown.

So for `gh pr create`, `gh pr edit`, and `gh issue create`: write the body to a
file first, then `--body-file /path/to/file`.

`dejima msg send` is the exception, because its payload is a positional
argument — `"$(cat file)"` is safe there, since command-substitution output is
not re-parsed as shell.

Be useful. Be specific. Commit as you go; don't accumulate large uncommitted
changes.

## Where the knowledge is

`docs/README.md` maps all 87 docs by the question you arrived with. Three are
worth reading before you diagnose anything or write a guard:
`docs/testing/guards-need-controls.md` (does this check have a SUBJECT?),
`docs/testing/readings-go-stale.md` (is this reading CURRENT?), and
`docs/testing/lifecycle-not-value.md` (WHEN does this code run? — the one that
decides whether a fix reaches things that already exist).

**A lesson that recurs twice becomes a check, not a third comment.** On
2026-09-03 five pieces of knowledge failed to propagate; every one was already
documented, correctly, and three were in the SAME FILE as the code that got them
wrong — one of them twenty lines above the edit. Proximity is not the fix,
because the reader is not failing to find the rule, they have a specific correct
belief that makes it feel inapplicable. If you are about to write a fourth
paragraph about something that keeps happening, write a gate instead;
`scripts/` has five and they work.
