# Lane P1 — ephemeral `-p/--profile` launch + profile CLI parity  (roadmap #4)

You are the **Profiles** agent for Dejima. Add an **ephemeral** `-p/--profile NAME` launch
flag and `dejima profile add/ls/switch` CLI parity. Independent — start now.

## Reality check (verify first)

Profiles/the switcher already exist (`internal/clientcfg/clientcfg.go`; `active_profile`
is referenced in `cmd/dejima/main.go`). So this is **not** "build profiles" — it's the
**ephemeral launch flag** + any missing CLI parity. Read `internal/clientcfg/clientcfg.go`
and the existing profile/switch code first; build only the gaps.

**The hard constraint — ephemeral means ephemeral:** `dejima -p cloud` selects a profile
for **this process only** and **MUST NOT write `active_profile`** to `client.json`.
Concurrent TUIs share `client.json`; writing the active profile would let one invocation
stomp another. Resolve the profile in-memory for the process; persist nothing.

**Scope:**
1. **`-p/--profile NAME`** persistent flag on the root command — ephemeral, in-memory only.
   Also surfaces the headless **`--host`** flag (per roadmap #4).
2. **`dejima profile add/ls/switch`** — CLI parity with whatever the TUI switcher does.
   `switch` is the *persistent* one (writes `active_profile`); `-p` is the ephemeral one.
   Don't duplicate the store — reuse `internal/clientcfg`.
3. A test proving `-p` does **not** mutate `client.json` (the anti-stomp guarantee).

**You own:** `cmd/dejima/profile.go` (+ test), the root-flag wiring in `main.go`, and
ephemeral-resolution helpers in `internal/clientcfg` (in a clearly-scoped new function).
**Do NOT touch:** install/uninstall, `internal/api/`. Keep `main.go` edits append-only.

**Workflow:** Own worktree, branch `feat/p1-profile`. Never `cd /workspace` or enter
another worktree. `go test ./...` + `golangci-lint run` (v2). Commit only your own hunks;
PR to `master` when green. Go 1.26.3.

**Done when:** `dejima -p NAME …` runs against that profile without persisting it (proven by
test), `--host` works headless, and `dejima profile add/ls/switch` reach parity with the TUI.
