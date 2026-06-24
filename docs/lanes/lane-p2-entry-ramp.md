# Lane P1/wave-2 — entry-ramp: adopt-existing-project wizard  (roadmap #8)

You are the **Entry-ramp** agent for Dejima. Today `dejima onboard` is host-setup-only.
Build a guided "adopt my existing project(s)" wizard that wraps the existing primitives.
Independent — start now.

## Reality check (verify first)

The primitive already exists: `--local-copy` seeds an island from the working copy on disk
(captures unpushed commits) instead of cloning origin — see `reposrc.Resolve(...)` used by
`dejima init` (`cmd/dejima/main.go` ~L1062/1170) and `dejima home` (`cmd/dejima/home.go`).
Auto credential-bind also exists (GitHub identities / provider creds APIs). **Your job is
the WIZARD around them, not new plumbing.** Read `cmd/dejima/onboard.go`, `home.go`, the
`init` path, and `internal/reposrc/` first.

**Scope:**
1. A guided flow (extend `onboard` or a new `dejima adopt`) that: detects/asks for existing
   local project path(s), creates an island per project seeded via the `--local-copy`
   primitive, and binds the right credentials (GitHub identity / provider keys) automatically.
2. Handle the common cases: a git repo with a remote, a git repo with unpushed work
   (`--local-copy` captures it), and a non-git dir. Confirm before creating anything.
3. Idempotent + interruptible (consistent with the Lane A install bar): re-running is safe.

**You own:** the wizard command (`cmd/dejima/adopt.go` or an `onboard` subcommand) + tests +
one append-only registration line in `main.go`. **Reuse, do not reimplement:**
`reposrc.Resolve` (local-copy), the island-create path, and the credential-bind APIs.
**Do NOT touch:** install.sh/uninstall, `internal/api/` grant routes, the volume layer.

**Workflow:** Own worktree, branch `feat/p1-entry-ramp`. Never `cd /workspace` or enter
another worktree. `go test ./...` + `golangci-lint run` (v2; master requires lint+build).
Commit only your own hunks; PR to `master` when green. Go 1.26.3.

**Done when:** a user with an existing local project runs one guided command and ends up
with an island seeded from their working copy (unpushed work included) and credentials
bound — confirmed before each create, safe to re-run.
