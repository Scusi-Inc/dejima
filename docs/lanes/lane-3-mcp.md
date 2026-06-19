# Lane 3 — MCP brokering

You are the **MCP** agent for Dejima. Build audited MCP (Model Context Protocol)
brokering — build-queue item #3. Deny-by-default MCP servers into islands, every
call ledgered. **Independent of Lanes 1 & 2 — start immediately.**

**Read first (in order):**
- `docs/roadmap.md` → "Committed build queue" + "Parallel lanes".
- **`docs/capability-broker-spec.md` + `docs/capability-brokering.md` + `internal/capability/` + `internal/api/capability.go`** — this is your model. MCP brokering is the **same pattern** (deny-all grants, per-island, typed, ledgered) applied to MCP servers. Mirror it.
- `docs/port-island-spec.md` (the brokering philosophy), `internal/project/` (grants).

**Scope, in order:**
1. **`internal/mcpbroker`** — per-island, deny-all grants of *named* MCP servers; declarative per-project. Model on `internal/capability`.
2. **API + CLI** — grant / revoke / list + the brokered execution path; `dejima mcp grant/revoke/ls`.
3. **Ledger** — write every MCP call as `mcp.*` entries to the **already-shipped** ledger (exactly like `capability.*` does). This is why you don't depend on Lane 1.

**You own:** `internal/mcpbroker`, `internal/api/mcp.go`, MCP-grant fields in `internal/project` (in a **separate file**), `cmd/dejima/mcp.go`.
**Do NOT touch:** `internal/ledger` internals (just call the existing append API, like capability does), `tokenauth.go`, audit code, `sdk/`. If you add endpoints, tell Lane 4 to spec them in `openapi.yaml`.

**Gates / seams:**
- No dependency on Lanes 1/2 — go now.
- Register routes via `RegisterMCP(mux)` (one line in `server.go`); keep your `project.Project` fields in a separate file (Lane 2 also touches that struct). Append-only.

**Workflow:** branch `feat/lane3-mcp`, own island/worktree — **not `master`**. `go test ./...`; extend `scripts/integration.sh` for the broker path. Commit your own hunks; PR to `master` when green. Go 1.26.3.

**Done when:** deny-all MCP grants per island; brokered MCP calls are ledgered (`mcp.*`); `dejima mcp` CLI works; tests + a live-Docker integration check pass.
