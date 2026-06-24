# Lane P1 — `dejima feedback`  (roadmap #9)

You are the **Feedback** agent for Dejima. Add `dejima feedback` — opens a public GitHub
issue via `gh`, carrying version + OS only, NO logs, shown before send, ledgered.
Greenfield; independent — start now.

**Read first:**
- `cmd/dejima/` command files for the house style — each command in its own file,
  registered with one line in `main.go` (e.g. `cmd/dejima/audit.go`, `link.go`, `port.go`).
- How other commands shell out / detect `gh` (onboard/provision use it). `internal/version`.
- The ledger append API (how `mcp`/`capability` write entries) — feedback must be ledgered.

**Scope:**
1. **`cmd/dejima/feedback.go`** — `dejima feedback [message]` (or prompt for the body).
   Compose an issue: title + body + a footer with **`dejima --version` + `runtime.GOOS`/arch
   only**. Absolutely no logs, paths, tokens, island names, or env.
2. **Show-before-send:** print the exact issue body and require explicit confirmation
   (honor a `--yes` and a `--dry-run`/print-only). On confirm, `gh issue create` against
   `aoos/dejima` (label e.g. `feedback`).
3. **Ledger** the action (an `feedback.*` or equivalent entry) like other operator acts.
4. Graceful when `gh` is missing/unauthed: print the composed issue + the URL to file it
   manually. Never block.

**You own:** `cmd/dejima/feedback.go`, `cmd/dejima/feedback_test.go`, one registration
line in `main.go`. **Do NOT touch:** install/uninstall, `internal/api/`. Keep the `main.go`
edit to a single append-only line (shared seam).

**Workflow:** Your own worktree on branch `feat/p1-feedback`. Never `cd /workspace` or enter
another agent's worktree. `go test ./...` + `golangci-lint run` (v2; master requires
lint+build). Commit only your own hunks; PR to `master` via `gh pr create` when green. Go 1.26.3.

**Done when:** `dejima feedback` composes a version+OS-only issue, shows it before sending,
files it via `gh` (or prints a manual fallback), is ledgered, and tests pass.
