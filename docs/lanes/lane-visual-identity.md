# Lane — per-island visual identity API (color + glyph)

You are the **Visual-identity** agent for Dejima. Add a backend for per-island visual
identity (a stable `color` + `glyph`) so an operator can override the TUI's deterministic
default. Assigned to d5 by a3; unblocks a2's editable-identity (#14). **Small + self-contained.
Scope guard: ONLY color + glyph — NOT a theming system.**

## Locked contract (confirmed with a2 — do NOT deviate from these field names)

a2's TUI (PR #128, merged) already computes a deterministic `islandIdentity(name)` default;
yours is the **override** it prefers when present.

- **Read** — `IslandInfo` gains a NESTED object, OMITTED when unset:
  ```json
  "identity": { "color": "#60a5fa", "glyph": "◆" }
  ```
  Exposed on BOTH `GET /v1/islands` and `GET /v1/islands/{name}`. Absent ⇒ no override ⇒
  a2 falls back to its default.
- **Set** — `PUT /v1/islands/{name}/identity` body `{ "color": "#60a5fa", "glyph": "◆" }`.
  Returns the **updated `IslandInfo`** (a2 wants instant reflect, no poll wait).
- **Clear** — `DELETE /v1/islands/{name}/identity` removes the override. Also returns the
  updated `IslandInfo`. (PUT always carries a valid pair; clearing is the DELETE.)
- **Validation:** `color` = hex (`#rgb` or `#rrggbb`); `glyph` = exactly one rune
  (`utf8.RuneCountInString(glyph) == 1`). Reject otherwise (400).
- **Auth + audit:** operator-scoped (`capOperate` in `roleauth.go`) — NOT in
  `tokenRouteAccess`, so a contained island token can never set its own identity. Ledger
  every set/clear like other island mutations (`identity.set` / `identity.clear`).

## Reality check / seams (verify, then build)

- **Persist** on the island: add an `Identity` (Color, Glyph) struct to `project.Project`
  with `toml:` tags (mirror the existing metadata fields ~`project.go:42-80`), saved via the
  existing `Project.Save()` into `~/.dejima/projects/<name>/config.toml`. No new store.
- **Expose:** populate `IslandInfo.Identity` where the payload is built —
  `info := IslandInfo{...}` at `internal/api/server.go:2271` (used by listIslands `:746` +
  getIsland `:759`). Add the field to the `IslandInfo` struct (`internal/api/types.go`,
  `json:"identity,omitempty"`) + a small `VisualIdentity` type.
- **Mutation pattern to mirror:** `updateIsland` (PATCH, `server.go:1021`) +
  `UpdateIslandRequest` (`types.go:262`) + its `roleauth.go:97` `capOperate` entry — the
  existing operator-only cosmetic-edit path. Your PUT/DELETE follow the same shape.
- **Ledger:** use `s.ledgerAppend(ledger.Entry{...})` (see `capability.go:76`).
- **Routes:** register `PUT`/`DELETE /v1/islands/{name}/identity` in `routes()`
  (`server.go`), add both to `roleauth.go` as `capOperate`, add `Client` methods
  (`SetIslandIdentity`, `ClearIslandIdentity`).
- **Spec:** add the routes + `identity` field to `openapi.yaml`; make the route-parity test
  pass (the SDK parity CI check counts daemon routes).

**You own:** the `Identity` field in `internal/project` (separate small struct), the
`IslandInfo` exposure, a new `internal/api/identity.go` (PUT+DELETE handlers + validation +
client methods), route + roleauth + openapi additions (append-only), tests.
**Do NOT touch:** install/uninstall, the grant routes, the wake/mailbox internals, other
agents' lanes. Keep `server.go`/`roleauth.go`/`openapi.yaml` edits append-only.

**Workflow:** Own worktree, branch `feat/visual-identity`. Never `cd /workspace` or enter
another worktree. `go test ./...` + `golangci-lint run` (v2; master requires lint+build);
shell passes shellcheck. Commit only your own hunks; PR to `master` when green. Go 1.26.3.

**Done when:** `GET` island payloads carry `identity:{color,glyph}` when set (omitted when
not); `PUT .../identity` validates + persists + ledgers + returns updated `IslandInfo`;
`DELETE .../identity` clears + ledgers + returns updated `IslandInfo`; both operator-only
(island token gets 403); hex + single-rune validation enforced; openapi parity green; tests pass.
