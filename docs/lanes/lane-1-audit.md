# Lane 1 — Audit core

You are the **Audit** agent for Dejima. Build the operational audit log +
read/export API + viewer — build-queue item #1, the governance moat.

**Read first (in order):**
- `docs/roadmap.md` → the "Committed build queue" and "Parallel lanes" sections (your gates + seams).
- `docs/security-boundary.md` (the exchange-down invariant your records describe).
- `internal/ledger/` — the **already-shipped** tamper-evident, hash-chained Port-crossing ledger. Extend its patterns; do not reinvent the chain.
- `internal/api/server.go` (route table), `internal/api/types.go` (shapes). Note: a `GET /v1/audit` route + `s.handleAudit` + `dejima audit --verify` already exist — build on them.

**Scope, in order:**
1. **Operational audit log** — append API requests + lifecycle events to the hash-chained `~/.dejima/ledger.jsonl` (reuse/extend `internal/ledger`). Opt-in, off by default, optional HMAC.
2. **Read/export API** — extend `/v1/audit` to read, filter, and export the record (not just verify); preserve chain verification.
3. **Viewer** — a TUI audit pane + a `dejima audit` read/export CLI (beyond `--verify`).

**You own:** `internal/ledger`, the audit API handler(s) (new file, e.g. `internal/api/audit.go`), the request-logging middleware, `cmd/dejima/audit.go`, the TUI audit view.
**Do NOT touch:** `internal/mcpbroker`, `openapi.yaml`, `sdk/`, role/scope code (Lane 2). Read identity off the request context (Lane 2 provides it) — don't build auth here.

**Gates / seams:**
- **Land the ledger append-interface + read API early** — Lane 2's activity feed depends on it.
- Coordinate "identity (who/role) on the request" with Lane 2 (they add it; you consume it).
- Register routes via a `RegisterAudit(mux)` so `server.go` changes are one line; put any config fields in a separate file. Append-only on shared files.

**Workflow — worktrees (read this):** You are one of several agents in a **shared island**. You start in **your own git worktree** (`/workspace/.agents/<your-id>`) on your own branch — **stay there.** Never `cd /workspace` (that's the primary/master worktree — commits there land on `master`) and never enter another agent's `.agents/<id>`. The repo's `.git` is shared across lanes, so branches are visible to all but your files live only in your worktree. Do your work on branch `feat/lane1-audit`; `go test ./...`; run `scripts/integration.sh` for ledger paths. Commit **only your own hunks**; rebase if a shared file conflicts; open a PR to `master` when green. Go 1.26.3; bump `api_version` if the contract changes.

**Done when:** events are recorded tamper-evidently; `dejima audit` reads/exports + verifies; the TUI viewer works; tests pass; a short doc note lands.
