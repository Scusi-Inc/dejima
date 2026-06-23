# Lane B — uninstall that doesn't nuke agents  (P0.2)

You are the **Uninstall-safety** agent for Dejima. Today bare `uninstall` purges every
island (deletes volumes), and `--keep-data` keeps config but **still destroys volumes** —
misleading. Make bare `uninstall` refuse and force an explicit choice, and **prove**
that a fresh install re-adopts pre-existing volumes + config. **Independent — start now.**

**Read first (in order):**
- **`cmd/dejima/main.go`** — the uninstall command (~L1973 onward: `islandAtRisk`, the
  `keepData`/`force`/`yes` flags, the "Type 'uninstall' to confirm" gate, the
  per-island purge loop). This is the code you change.
- `internal/service/service.go` (host-service uninstall: `ActionUninstall`),
  `internal/project/` (where `~/.dejima/projects` config + named-volume bookkeeping live),
  `internal/runtime/` (volume create/remove — the Docker named-volume layer).
- The purge-guard: `internal/api/purge_guard_test.go` + `islandAtRisk` (mirror the
  daemon's at-risk check; don't half-uninstall).

**Scope, in order:**
1. **Bare `uninstall` refuses** and forces an explicit choice — no destructive default.
2. **`--keep-islands`**: remove daemon + binaries + containers; **KEEP** named volumes +
   `~/.dejima/projects` config. (Reinstall re-adopts.)
3. **`--purge-all`**: today's nuke behavior, now opt-in only.
4. **Fix `--keep-data`**: it keeps config but still destroys volumes. Re-map it to the
   keep-islands semantics or remove it — no flag may lie about what it deletes.
5. **PROOF (the acceptance bar, not the flag logic):** after `--keep-islands`, a fresh
   install **re-adopts** the pre-existing named volumes + `~/.dejima/projects` config and
   the islands come back. Demonstrate it.

**You own:** the uninstall command block in `cmd/dejima/main.go` (and a new
`cmd/dejima/uninstall_test.go` if you split it out), uninstall-path code in
`internal/service/` and the volume-retain/re-adopt path in `internal/project/` /
`internal/runtime/` (keep changes in clearly-scoped functions).
**Do NOT touch:** `install.sh` / onboard (Lane A — but the re-adopt path you prove is
exercised *by* a Lane A install; coordinate on the seam, don't edit their files),
`internal/api/` grant routes (Lane C).

**Gates / seams:**
- No code dependency on A or C — go now. The re-adopt proof uses a normal install; you
  don't need Lane A's changes to test re-adoption against the current installer.
- **Exit-ramp is OUT OF SCOPE** — extracting volumes → plain host dirs (run without
  Dejima) is a separate **P1** task. `--keep-islands` already prevents lock-in (volumes
  survive + re-adopt); do not build extraction here.
- The **virgin-Mac re-adopt proof** is run by **Lane 0 (human)**: ship a Go test +
  `scripts/integration.sh` Docker check for what you can verify in-island, plus a
  **documented re-adopt proof procedure** (install → create island → `--keep-islands`
  uninstall → reinstall → island re-adopted) in your PR for Lane 0 to run on a clean Mac.

**Workflow — worktrees (read this):** You are one of several agents in a **shared
island**, in **your own git worktree** on your own branch — **stay there.** Never
`cd /workspace` and never enter another agent's worktree. Branch `feat/lane-b-uninstall`.
`go test ./...` + `golangci-lint run` (CI lint = golangci-lint v2; master requires
lint+build). Commit only your own hunks; rebase on conflict; PR to `master` when green.
Go 1.26.3.

**Done when:** bare `uninstall` refuses and forces a choice; `--keep-islands` keeps
volumes + config and a reinstall re-adopts (demonstrated via your documented procedure);
`--keep-data` no longer lies; `--purge-all` is the only path that nukes.
