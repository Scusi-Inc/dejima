# Lane 2 — Team auth & roles

You are the **Team auth** agent for Dejima. Build token issuance + 3 roles +
per-island scope — build-queue item #2, the solo→team conversion bridge.

**Read first (in order):**
- `docs/roadmap.md` → "Committed build queue" + "Parallel lanes" (your gates + seams).
- `docs/security-boundary.md` — **the non-negotiable exchange-down invariant.** Tokens are attenuated; no god-token; master identity never reaches an island. Conform to it exactly.
- `internal/api/tokenauth.go` (the existing per-island token middleware — extend it), `internal/project/` (where role/scope will live), `internal/api/server.go`.

**Scope, in order:**
1. **`dejima token` (create / list / revoke)** — issue `owner` tokens carried via env/header; complements Tailscale identity, doesn't replace it.
2. **Three roles + per-island scope** — `owner` / `operator` (lifecycle, no purge) / `viewer` (read+observe); a token can be limited to specific islands. Enforce in the auth middleware.
3. **Identity on the request context** — put who/role on each request so Lane 1's audit log can record it. **Land this early** (Lane 1 needs it).

**You own:** `internal/api/tokenauth.go`, role/scope fields in `internal/project` (in a **separate file**, not the main struct edit if avoidable), `cmd/dejima/token.go`, the TUI roles bits.
**Do NOT touch:** `internal/ledger` internals, `internal/mcpbroker`, `sdk/`. If you add/rename endpoints, tell Lane 4 to update `openapi.yaml` — don't edit it yourself.

**Gates / seams:**
- Identity-on-request lands early (gates Lane 1's enriched records).
- The **activity feed** is the shared last item — gated on both your roles and Lane 1's audit log; don't start it until both exist.
- Register routes via `RegisterAuth(mux)` (one line in `server.go`); config fields in a separate file (Lane 3 also touches `project.Project` — keep your fields isolated). Append-only.

**Workflow:** branch `feat/lane2-team-auth`, own island/worktree — **not `master`**. `go test ./...`; run `/security-review` on the auth surface before PR. Commit your own hunks; PR to `master` when green. Go 1.26.3.

**Done when:** tokens issue/list/revoke; 3 roles + island scope enforced in middleware; identity is on the request context; security-reviewed; tests pass.
