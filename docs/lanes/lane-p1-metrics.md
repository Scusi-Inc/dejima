# Lane P1 — install/download metrics, Option B  (roadmap #10)

You are the **Metrics** agent for Dejima. Ship an **install-totals** view aggregated from
**public download counts** — GitHub releases + npm + PyPI. Independent — start now.

## Why Option B (read this — the obvious plan is broken)

The roadmap said "count the existing update-check pings server-side as the active-user
proxy." **That is impossible:** the client's update check hits
`https://api.github.com/repos/aoos/dejima/releases/latest` **directly**
(`internal/selfupdate/selfupdate.go`) — it never reaches a Dejima-owned server, so there
are no pings for us to count. Adding a Dejima `version` endpoint (the true active-user
proxy) is deferred (Option A). **This lane is Option B: aggregate the public download
counts we already have.** No client change, no new client IDs, no new pings.

**Scope:**
1. **Aggregator** — a script/command (`scripts/metrics/install-totals.sh` or a small
   `go run ./...` tool; match house style) that fetches and sums:
   - **GitHub release assets:** `GET api.github.com/repos/aoos/dejima/releases` →
     sum `assets[].download_count` (per-asset + per-release + grand total).
   - **npm:** `GET api.npmjs.org/downloads/point/last-month/dejima` (and `@dejima/sdk`).
   - **PyPI:** the `dejima` SDK package via pypistats (or the BigQuery/pepy endpoint —
     pick the simplest public one; document the choice).
2. **Output** — a readable summary (table or JSON) of install/download totals by channel.
3. **Document the known gap** — **Homebrew tap installs are invisible** (no public
   per-install count). State it plainly in the output + a short doc/README note so the
   number is never mistaken for "all installs."
4. Disclosed + honest: this uses only already-public aggregate stats; note that it is
   *downloads/installs, not active users* (active-user proxy = the deferred Option A).

**You own:** `scripts/metrics/` (or a `cmd/`-style tool dir) + a short doc
(`docs/metrics-install-totals.md`). **Do NOT touch:** `internal/selfupdate` behavior,
install/uninstall, `internal/api/`. This is read-only aggregation of public data — add no
client-side telemetry.

**Workflow:** Own worktree, branch `feat/p1-metrics`. Never `cd /workspace` or enter
another worktree. If you add Go code, `go test ./...` + `golangci-lint run` (v2); if
shell, it must pass `shellcheck` (wired in CI). Commit only your own hunks; PR to `master`
when green. Go 1.26.3.

**Done when:** one command prints install/download totals across GitHub + npm + PyPI, the
Homebrew-invisible gap is stated in the output + doc, and it's clear these are
downloads-not-actives. No client telemetry added.
