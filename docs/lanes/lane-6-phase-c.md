# Lane 6 — Phase C (thorough all-features deterministic suite + freshness gate)

Build the **thorough deterministic test of every feature** + the mechanism that keeps it
**fresh**. Full design + the source-of-truth/TUI/onboarding answers:
**`docs/testing/full-suite-design.md`** (read first), plus `docs/testing/test-coverage-matrix.md`
(the ~150-row checklist to complete), `openapi.yaml` (API truth, 88 ops), the cobra CLI tree,
and the bubbletea TUI.

## Scope (each its own PR; tests/CI only — no product behavior change)

1. **API+CLI coverage gate (the freshness mechanism) — highest priority.** A CI check that
   enumerates every `openapi.yaml` operation AND every cobra command and asserts each has ≥1
   referencing test; **fail CI when new surface has no test.** Extend the existing route-parity
   test. This is what keeps the suite current as features land.
2. **CLI — 100% command coverage.** Table tests for every command not yet covered (exit code +
   output + the API call), against an in-proc/httptest daemon. Drive the matrix CLI rows to `A`.
3. **TUI — teatest for every screen + key action** (island list, agent menu, all confirm
   pop-ups incl. the type-the-name gate, audit pane, link/mailbox surfaces, the `m` action
   menu, help, update). Drive the §17 matrix rows to `A`.
4. **Deterministic full-feature run** wired into the live suite (extend `integration.sh` /
   the tier scripts) so a single dispatch exercises **every** feature once, thoroughly, in a
   sensible order, with clear per-feature pass/fail.
5. **Structured reporting:** emit a machine-readable summary (pass/fail per feature) as a CI
   artifact, and on failure **open/update a GitHub issue** with the failing detail.

## Phase C2 (attempt if time; else leave a clear stub + flag for follow-up)

6. **Live TUI-drive + Claude screen-analysis:** a script/agent that walks the real TUI in
   tmux (`send-keys` + `capture-pane`) and feeds each frame to Claude to judge correctness/
   clarity/glitches. Gate behind `TEST_AGENT_KEY`.
7. **Onboarding self-test:** run `onboard --provision-host` (idempotent/dry-run), **time it
   (assert < 5 min)**, capture prompts, have Claude judge "dead-simple? friction points?".

## Rules

- Tests/CI only; tiny justified seams. Branch from origin/master by ref; own isolated worktree.
- Every PR: `go build`/`vet`/`golangci-lint`/`go test` green; shellcheck-clean scripts; valid YAML.
- MERGE POLICY: self-merge **test-only** PRs on green (poll: ≥2 checks, 0 pending, none failed;
  `gh pr update-branch` if stale; never `--admin`). Any **.github/workflows** change → CREATE +
  flag for orchestrator review, do not self-merge.
- Flip each `test-coverage-matrix.md` row's `Now` → `A` as you cover it.
- The live-on-Mac-mini run is the OPERATOR's step (no Docker/macOS here) — author + statically check.

## Report back
PRs (merged/open-for-review), matrix rows driven to `A`, whether the coverage gate is live + how
it enforces, and what (if any) C2 work you stubbed vs built.
