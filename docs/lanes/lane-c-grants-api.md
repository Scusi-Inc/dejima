# Lane C — grants API for the TUI "grants" view  (P0.3)

You are the **Grants-API** agent for Dejima. **Read the "Reality check" first — the
roadmap's premise is stale.** Frontend P1.8 (the TUI grants view) consumes this lane.

## Reality check — what already exists (verified 2026-06-23)

The roadmap says "expose Port + MCP + inter-island links over the daemon API (MCP is
CLI-only today)." **That is no longer true.** All four grant-list endpoints already
exist, are wired in `server.go`'s `routes()`, and are operator-readable (`capRead` in
`roleauth.go`):

- `GET /v1/islands/{name}/port/scopes`        → `handleListPortScopes`  (port grants)
- `GET /v1/islands/{name}/capability/grants`  → `handleListCapabilityGrants`
- `GET /v1/islands/{name}/mcp/grants`         → `handleListMCPGrants` (via `RegisterMCP`)
- `GET /v1/links`                              → `listLinks` (inter-island links)

So this is **not** a "build the MCP API" lane. The real, thinner gap is a single
**unified per-island grants view** the TUI can read in one call, plus spec parity and a
deny-all verification. Confirm the four endpoints above still behave as described before
you build — if any is missing/renamed, that becomes your first task.

**Read first (in order):**
- `internal/api/server.go` (`routes()` + `RegisterMCP`), `internal/api/roleauth.go`
  (`tokenRouteAccess` / `capRead`), and `internal/api/{port,capability,mcp,link}.go`.
- `openapi.yaml` + the route-parity CI test (Lane 4 SDK seam) — every route must be in
  the spec or the parity test fails.
- The TUI grants-view consumer (the frontend P1.8 target) so the unified shape fits it.

**Scope, in order:**
1. **Unified grants view.** Add `GET /v1/islands/{name}/grants` returning all grant
   types for one island in a single typed shape (`{port:[], capability:[], mcp:[],
   links:[]}` — links filtered to the ones touching this island). Operator-only
   (`capRead`). This is the one call the TUI makes instead of stitching four. Mirror the
   existing handlers' view structs; don't duplicate their stores.
2. **OpenAPI + SDK parity.** Add the new route (and any of the four that are missing) to
   `openapi.yaml`; make the route-parity test pass. Add the client method (`api.Client`).
3. **Verify deny-all is genuine.** Confirm a fresh island returns empty grants for all
   four types (not a convenience default like mount-$HOME). Add a test that asserts it.

**You own:** a new `internal/api/grants.go` (the aggregate handler + view struct) + its
route line in `server.go` + its `roleauth.go` entry + its `openapi.yaml` block + a
client method. Append-only edits to `server.go` / `roleauth.go` / `openapi.yaml`.
**Do NOT touch:** the existing `port.go` / `capability.go` / `mcp.go` / `link.go`
handler internals (read + reuse their view builders), `install.sh` (Lane A), the
uninstall block (Lane B).

**Gates / seams:**
- No dependency on A or B — go now.
- `server.go` and `roleauth.go` are shared seams — keep your additions to append-only
  lines and commit only your own hunks.
- If Lane A changes any daemon route, reconcile in `openapi.yaml`.

**Workflow — worktrees (read this):** You are one of several agents in a **shared
island**, in **your own git worktree** on your own branch — **stay there.** Never
`cd /workspace` and never enter another agent's worktree. Branch `feat/lane-c-grants-api`.
`go test ./...` + `golangci-lint run` (CI lint = golangci-lint v2; master requires
lint+build). Commit only your own hunks; rebase on conflict; PR to `master` when green.
Go 1.26.3.

**Done when:** `GET /v1/islands/{name}/grants` returns all four grant types in one shape,
operator-gated, in `openapi.yaml` with the parity test green, with a test proving a
fresh island is deny-all across all four.
