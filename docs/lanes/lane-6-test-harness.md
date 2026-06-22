# Lane 6 — automated test harness (Phase A)

You are the **Test-Harness** agent. Build **Phase A** — everything that needs **no
external accounts** and catches regressions **on every PR**. The goal is to start
replacing the manual Minion verification pass with automation. Full design + tiers +
phasing: **`docs/testing/automated-test-harness.md`** (read it first), plus
`scripts/integration.sh` (the existing Docker suite — your Tier-2 base), `cmd/dejima`
(CLI), the bubbletea TUI, and `internal/api` (handlers).

## Phase A scope (each its own small PR)

1. **TUI tests via `teatest`** (charmbracelet's bubbletea test harness — confirm the
   current import path). Model the main views — island list, agent menu, the confirm
   pop-up, the audit pane, and any link/mailbox surfaces — by feeding key events and
   asserting rendered frames + state transitions. Drive the bubbletea model directly; no
   daemon needed.
2. **CLI coverage.** Table tests invoking the major commands against a fake / in-proc
   daemon (or an `httptest` server): assert exit codes + output for `ls`, `agent`, `port`,
   `link`, `msg`, `audit`, `token`.
3. **API handler tests.** Extend `httptest` coverage for routes not yet covered —
   `link`, `mailbox`, `link_action`, `wake`.
4. **Expand `scripts/integration.sh` into the full Tier-2 matrix**, factored to run on any
   Docker host and emit a clear pass/fail report; add a `make test-integration` target:
   lifecycle (init/clone/exec/hibernate/wake/upgrade/purge + the unpushed-work guard),
   Port (intake/export/traversal-refusal/ledger), MCP (grant/call/ledger/revoke), audit
   (record/verify/export), and **inter-island** (deny-all / grant / message / action /
   approve / deny / fail-closed). Shellcheck-clean.
5. **Wire a Tier-1 CI job** so the `teatest`/CLI/API tests run in `ci.yml` on every PR
   (they need no Docker). Add a **stubbed, disabled nightly workflow** scaffold for Tiers
   2–4 so Phase B just fills it in. *(Pushing `.github/workflows` needs the gh `workflow`
   token scope — see the gh-workflow-scope note if the push is rejected.)*

## Do NOT build (Phases B/C — need accounts/hardware)

The self-hosted runner config, the Tier-3 macOS suite (`service`/`onboard`/Keychain/
idle-hibernate/wake/ssh), and the Tier-4 real-agent smoke. Leave clearly-marked
`TODO(phase-b)` stubs + the gated, disabled nightly workflow so Phase B drops in cleanly.

## Constraints

- **Tests only** — don't change product behavior. If you need a test seam, keep it tiny
  and justify it in the PR.
- The build island has **no Docker**, so the `integration.sh` expansion is authored +
  shellcheck-checked here and **run live on Minion** (flag it for the operator).
- Per-PR branches; `go build`/`vet`/`golangci-lint run ./...`/`go test ./...` green; small PRs.
