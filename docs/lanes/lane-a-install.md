# Lane A — bulletproof install + first-run  (P0.1 — THE launch gate)

You are the **Install** agent for Dejima. Show HN will hammer the install path; the
#1 failure mode is a broken install on a clean Mac. Make `curl|sh`, `brew`, and `npm`
all work first-try on a virgin Mac, and make the onboard wizard reach a running island
or a clear, actionable failure — never a dead end. **Independent lane — start now.**

**Read first (in order):**
- `docs/launch-checklist.md` and `docs/roadmap.md` → launch items.
- **`install.sh`** (the `curl|sh` path, incl. the Docker/colima bootstrap),
  **`homebrew/dejima.rb`** + **`scripts/gen-homebrew-formula.sh`** (brew tap; CI
  auto-bump already wired, PR #36), **`npm/`** (`install.js`, `bin/`, `package.json`).
- **`cmd/dejima/onboard.go`** + `cmd/dejima/provision.go` + `cmd/dejima/doctor*.go`
  — the first-run wizard, the adaptive first-run, and the connection-failure offer
  (Wave 0.5, shipped). `internal/service/` (host service install).

**Scope, in order:**
1. **Three channels × clean Mac × first-try.** Walk each install path as a brand-new
   user with no prereqs: `curl|sh`, `brew install`, `npm i -g dejima`. Every prereq
   the script can't satisfy must be detected and clearly instructed, not assumed.
2. **Onboard wizard has no dead ends.** Every branch reaches a running island OR a
   failure message that names the cause + the next step. Re-exercise the adaptive
   first-run and the connection-failure-offer paths.
3. **No-lost-work / idempotent.** Interrupt (Ctrl-C / kill) mid-install and
   mid-first-run; confirm no half-state bricks a retry. Re-running install is safe.
4. **Failure-message audit.** Show HN users won't read logs — every non-zero exit
   names the cause and the fix.

**You own:** `install.sh`, `install-client.sh`, `homebrew/dejima.rb`,
`scripts/gen-homebrew-formula.sh`, `npm/`, `cmd/dejima/onboard.go`,
`cmd/dejima/provision.go`, `cmd/dejima/doctor*.go` (onboarding-adjacent only).
**Do NOT touch:** the uninstall command (`cmd/dejima/main.go` uninstall block ~L1973+
— that's Lane B), `internal/api/` grant routes (Lane C). If you change a daemon route,
tell Lane C so it stays in sync with `openapi.yaml`.

**Gates / seams:**
- No code dependency on B or C — go now.
- The **virgin-Mac proof** is run by **Lane 0 (human)**. You cannot run a truly clean
  Mac inside this island, so: (a) add the in-island/Docker checks you *can* run to
  `scripts/integration.sh`, and (b) write a **step-by-step virgin-Mac proof procedure**
  in your PR for Lane 0 to execute (one section per channel + the interrupt/retry case).

**Workflow — worktrees (read this):** You are one of several agents in a **shared
island**, in **your own git worktree** on your own branch — **stay there.** Never
`cd /workspace` (the primary/master worktree — commits there land on `master`) and
never enter another agent's worktree. Branch `feat/lane-a-install`. Run `go test ./...`
+ `golangci-lint run` (CI lint = golangci-lint v2; branch protection on master requires
lint+build). Commit only your own hunks; rebase on shared-file conflict; PR to `master`
when green. Go 1.26.3.

**Done when:** three channels install first-try on a clean Mac (proven by Lane 0 via
your documented procedure), the onboard wizard has no dead ends, and interrupt/retry
leaves no broken state.
