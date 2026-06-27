# Lane 0 — install/uninstall verification harness (launch gate)

You are the **Verify-harness** agent for Dejima. The P0 launch gate (install #86,
uninstall `--keep-islands` #89, grants #85) is code-complete but **UNVERIFIED on a real
clean Mac** — the single biggest remaining launch risk. Build the harness that proves it
**repeatably**. Assigned to d5 by a3 (planning). Time-sensitive.

## Reality check (verify first — confirmed 2026-06-24)

ASSEMBLE existing pieces; don't rebuild them:
- **`scripts/install-channels-check.sh`** — Lane A's 21-assertion install-channel check
  (asset-name agreement, curl-path guard, formula==generator). In-island/CI part exists.
- **`scripts/integration.sh`** — already has a *"uninstall --keep-islands + re-adopt"*
  feature (the Lane B acceptance bar, ~L692): create island + marker → uninstall keep-islands
  → rebuild+restart daemon → assert island re-adopts + marker survives. Real Docker.
- **`docs/operator-tests/uninstall-keep-islands-readopt.md`** + the virgin-Mac procedures in
  PR #86's body — the documented manual proofs to automate.
- **`docs/testing/test-coverage-matrix.md`** — flip rows as they go green.
- **Workflows:** `.github/workflows/nightly.yml` exists but runs in-container. There is **no
  `nightly-live.yml`** — you create it (a real **macOS runner**; see
  `docs/testing/dejimaqa-runner-setup.md` for the Minion/dejimaqa runner).

## Scope

1. **Clean-Mac driver** — a scripted, idempotent `teardown → provision` for a clean macOS
   test env (fresh user / VM / ephemeral), so each run starts virgin (no Docker/colima/brew/
   `~/.dejima` history). Document the reset procedure.
2. **Automated proof loop** (one operator-runnable script): for EACH channel
   (`curl|sh`, `brew`, `npm`): teardown → install → assert daemon up + a test island running
   → `dejima uninstall --keep-islands` → reinstall → **assert volumes + `~/.dejima/projects`
   re-adopted** (island comes back, marker survives). Reuse `install-channels-check.sh` +
   the `integration.sh` re-adopt feature; don't duplicate their assertions.
3. **`.github/workflows/nightly-live.yml`** — runs the loop on a real macOS runner, nightly.
   Gate it so it only runs where a clean Mac runner exists; fail loud, capture logs.
4. **Flip `docs/testing/test-coverage-matrix.md`** rows for install-channels + uninstall
   re-adopt as they're covered.

## Hard constraint — you can't self-verify

The LIVE run needs a clean macOS host (Minion) and is **operator-gated**. You build the
harness + assertions and make them **fully scripted / CI-runnable**; the **operator runs it
on Minion**. Do NOT claim the live run passed — deliver the harness + a hand-off doc telling
the operator exactly how to run it and what green looks like.

**You own:** the clean-Mac driver script(s), the proof-loop script, `nightly-live.yml`, the
coverage-matrix edits, and a short operator hand-off doc. **Reuse, don't rebuild:**
`install-channels-check.sh`, the `integration.sh` re-adopt feature. **Do NOT touch:**
`install.sh`/uninstall code (shipped), `internal/`, PR #124 / `feat/p1-skew-detection`
(another agent's lane), the grant routes.

**Workflow:** Own worktree, branch `feat/lane0-verify-harness`. Never `cd /workspace` or
enter another worktree. Shell must pass `shellcheck` (wired in CI); any Go passes
`go test ./...` + `golangci-lint run` (v2). Pushing `.github/workflows/` needs the gh
`workflow` scope — if the push is rejected for scope, say so in your report (don't silently
drop the workflow). Commit only your own hunks; PR to `master` when green. Go 1.26.3.

**Done when:** one operator-runnable harness drives teardown→install(each channel)→assert→
uninstall --keep-islands→reinstall→assert re-adopt on a clean Mac; `nightly-live.yml` runs it
on a macOS runner; the coverage matrix reflects it; and a hand-off doc tells the operator how
to run it on Minion + what green looks like. (The live run itself is the operator's, not yours.)
