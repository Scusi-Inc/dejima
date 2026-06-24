# Lane P1/wave-2 — TUI scope-picker (Port grants on a running island)  (roadmap #6)

You are the **Scope-picker** agent for Dejima. Add a TUI view to add/remove Port scopes
(brokered host paths) on a running island. Independent — start now.

## Reality check (verify first — the backend is DONE)

The roadmap calls this "scope picker backend," but the backend already exists:
- **API:** `POST`/`GET`/`DELETE /v1/islands/{name}/port/scopes`
  (`internal/api/port.go`), operator-gated (`capOperate`/`capRead` in `roleauth.go`).
- **CLI:** `dejima port grant/list/revoke` (`cmd/dejima/port.go`).
- **Deny-all default is CONFIRMED** — the CLI help states "Access is deny-all by default";
  a fresh island has no scopes. Add a regression test if one is missing, but don't redesign.

**The only gap is the TUI** — there is no TUI scope view today. So this is a frontend lane.

**Read first:** `cmd/dejima/tui*.go` (the bubbletea model + existing per-island views —
follow their patterns), `internal/api/port.go` + the `Client` port-scope methods,
`cmd/dejima/port.go` (the exact grant/list/revoke semantics, `:ro`/`:rw` modes), and Lane
C's **unified grants endpoint** `GET /v1/islands/{name}/grants` (`internal/api/grants.go`) —
the scope view can read Port grants from there alongside the other grant types.

**Scope:**
1. A TUI scope-picker for the selected island: list current Port scopes, add a scoped host
   path (`:ro`/`:rw`), and revoke one — calling the existing `Client` methods. Operator-only
   (viewers can't mutate), matching the API's auth.
2. Make deny-all visible: an island with no scopes shows "deny-all: no host files reachable"
   (mirror the CLI's wording), so the security posture is obvious in the UI.
3. Wire it into the existing TUI navigation consistently with other per-island sections.

**You own:** new TUI scope-view file(s) under `cmd/dejima/` (e.g. `tui_scope.go`) + tests +
the navigation wiring. **Reuse, do not reimplement:** the `Client` port-scope methods and
(optionally) the unified grants endpoint. **Do NOT touch:** `internal/api/port.go` internals
(call the client), install/uninstall, the volume layer.

**Workflow:** Own worktree, branch `feat/p1-scope-picker`. Never `cd /workspace` or enter
another worktree. `go test ./...` (teatest for the TUI, matching existing `tui_*_test.go`) +
`golangci-lint run` (v2; master requires lint+build). Commit only your own hunks; PR to
`master` when green. Go 1.26.3.

**Done when:** the TUI can list/add/revoke Port scopes on a running island via the existing
API, deny-all is shown clearly for a fresh island, mutations are operator-only, and teatest
coverage passes.
