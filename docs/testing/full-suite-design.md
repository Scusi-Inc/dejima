# Full-feature suite, source-of-truth, and TUI/UX testing

How `dejimaqa` runs a **thorough deterministic test of every feature** (CLI + API + TUI +
the onboarding flow), keeps that coverage **fresh as features are added**, and uses **Claude
to judge the TUI + the setup UX**. The randomized "soak/combination" backbone is a *later*
layer (roadmapped) — this doc is the deterministic all-features pass that comes first.

## Source of truth + freshness (so new features can't slip through untested)

**The API (`openapi.yaml`) + the CLI command tree are the truth.** Both are machine-
enumerable, and the TUI's actions map onto API calls.

- Today: a **route-parity** CI test already keeps `server.go` ↔ `openapi.yaml` in sync (88 ops).
- Add a **coverage gate** (CI): assert every `openapi` operation AND every cobra command has
  ≥1 referencing test. **A new route or command with no test fails CI.** That's the freshness
  guarantee — you cannot merge a new feature without a test, so the suite never drifts.
- `test-coverage-matrix.md` is reconciled against (ideally generated from) this enumeration,
  so the human-readable list stays honest too.

## CLI — full coverage

Extend the Phase-A CLI table tests to **every** command: exit code + output + the API call it
issues, against an in-proc daemon. The coverage gate enforces one-test-per-command.

## TUI — two levels

1. **teatest (logic, runs in CI):** drive the bubbletea model directly; assert frames + state
   transitions for every screen and key action. No daemon.
2. **Live TUI + Claude screen-analysis (the novel layer):** an agent launches the *real* TUI
   in tmux, walks every screen with `tmux send-keys`, grabs each rendered frame with
   `tmux capture-pane`, and feeds it to **Claude** to judge — is the screen correct, clear,
   glitch-free; does each action do what its label says; any regression a fixed assertion
   would miss. This is **LLM-as-UX-judge over captured frames**. *Yes — the agent can both
   operate the TUI (send-keys) and "see" it (capture-pane), which is exactly the inject path
   wake-on-message uses.*

## Onboarding self-test — "dead-simple" + under 5 minutes

The agent runs the Mac-mini setup flow (`onboard --provision-host`, idempotent/`--reset`
mode so it doesn't re-install packages), **times the automatable steps (assert < 5 min)**,
captures each prompt/screen, and has **Claude judge "is this dead-simple? where's the
friction?"** Output: pass/fail on the time budget **+** a concrete Claude UX critique
(friction points, confusing prompts, missing guidance). The full from-scratch provision stays
the system-mutating/opt-in path; the dry-run times the wizard's own flow.

## Reporting back

Each run emits a **structured summary** (pass/fail per feature + the Claude UX findings) as a
CI artifact, and **auto-files/updates a GitHub issue on failure** with the failing detail —
so the orchestrator reads results + repros via `gh`, no log-scraping.

### Implementation

`scripts/lib/report.sh` is the shared reporting layer the live suites source. It adds:

- `feature "<name>"` — opens a feature block (also prints the bold banner), and
- `pass`/`fail` versions that tally per-feature **and** globally, capturing the first
  failure detail per feature, then
- `report_summary` — prints the per-feature rollup and, when `DEJIMA_REPORT=<path>` is set,
  writes a machine-readable JSON summary `{suite, passed, failed, features:[{name, status,
  passed, failed, detail}]}` (emitted by hand — no `jq` dependency on the runner). It returns
  non-zero if any feature failed.

`scripts/integration.sh` is the **deterministic full-feature run**: a single dispatch
exercises every Tier-2 feature once, in order, each tagged with `feature` so the JSON has one
row per feature. The CI step uploads that JSON as an artifact and, on failure, opens/updates a
GitHub issue from it (the `.github/workflows` wiring is reviewed separately — it's the one part
the harness author does not self-merge). The same `report.sh` is reusable by the tier3/tier4
suites by setting their own `DEJIMA_SUITE`.

## Triggering (already exists)

No missing piece for the deterministic suite: `gh workflow run nightly.yml -f …` triggers a
run on the `macos-mini` runner and `gh run view` reads the result (the runner must be up).
The agent-driven adaptive layer (a QA agent reachable over the inter-island channel) is the
later "fix-loop" enhancement — see the harness backbone notes.

## Phasing

- **Phase C (now):** the thorough deterministic suite — full CLI coverage, teatest for every
  screen, the **API+CLI coverage gate** (freshness), structured reporting.
- **Phase C2 (next):** live TUI-drive + **Claude screen-analysis** + the onboarding UX
  self-test (timer + Claude judgment).
- **Later (roadmap):** the randomized **soak/combination backbone**; the adaptive **QA-agent
  fix-loop** over the inter-island channel.
