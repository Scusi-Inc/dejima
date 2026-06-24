# Lane P1/wave-2 — exit-ramp: extract volumes → plain host dirs  (no-lock-in)

You are the **Exit-ramp** agent for Dejima. Ship the no-lock-in escape hatch: extract an
island's volumes to plain host directories so the work keeps running **without Dejima**.
This was pulled out of the uninstall lane (P0.2) as its own task. Independent — start now.

## Context

`dejima uninstall --keep-islands` (shipped, PR #89) already prevents lock-in *within*
Dejima — named volumes survive and a reinstall re-adopts them. **Exit-ramp is the stronger
guarantee: get your code/state OUT to the host filesystem entirely.** Read
`internal/runtime/docker.go` + `runtime.go` (the volume layer: `EnsureVolume` etc.),
`cmd/dejima/uninstall.go` (the keep-islands re-adopt model + deterministic volume names
`dejima-<island>-workspace` / `-home`), and `cmd/dejima/main.go`'s clone path (it already
copies workspace+home volumes — a model for reading volume contents).

**Scope:**
1. A command (e.g. `dejima eject <island> <dest-dir>` or `dejima export`) that copies the
   island's **workspace** (code + git history) and optionally **home** (tool creds/state)
   volume contents into plain host directories under `<dest-dir>`.
2. The extracted workspace must be a usable git working tree the user can `cd` into and run
   without Dejima. State clearly what's portable vs. island-specific.
3. Safe + idempotent: don't mutate the source volume; refuse to clobber a non-empty dest
   without a flag; best run when the island is idle (warn otherwise), mirroring `clone`.
4. Document the exit-ramp in a short doc — this is a load-bearing part of the
   containment/no-lock-in thesis, so make the guarantee explicit.

**You own:** the eject/export command (`cmd/dejima/eject.go` or similar) + tests + one
append-only `main.go` registration line + a short doc. **Reuse** the volume read/copy
approach from the existing `clone` path; do not change `EnsureVolume`/uninstall behavior.
**Do NOT touch:** install.sh, the uninstall command logic (Lane B, shipped),
`internal/api/` grant routes.

**Workflow:** Own worktree, branch `feat/p1-exit-ramp`. Never `cd /workspace` or enter
another worktree. `go test ./...` + `golangci-lint run` (v2; master requires lint+build).
Commit only your own hunks; PR to `master` when green. Go 1.26.3.

**Done when:** one command extracts an island's workspace (and optional home) to host dirs,
the result is a usable git tree runnable without Dejima, the source volume is untouched,
clobber is guarded, and the no-lock-in guarantee is documented.
