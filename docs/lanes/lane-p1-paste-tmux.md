# Lane P1 — tmux.conf hardening + host→island image-paste bridge  (roadmap #7)

You are the **Paste/tmux** agent for Dejima. Two related deliverables; **land #1 first
(small, independently valuable), then #2.** Independent lane — start now.

## Deliverable 1 — ship a tmux.conf (do this first)

Ship a tmux config with **`set -g allow-passthrough on`** + **extended-keys** enabled.
This also fixes **Shift+Enter** and desktop **notifications** passing through tmux.

- Find where Dejima launches/attaches tmux (agent sessions + host terminals —
  `internal/hostterm/`, `cmd/dejima/term.go`, the session attach path). Ship a config that
  applies to the sessions Dejima creates (a committed `tmux.conf` referenced on session
  create, or `-f`). Don't clobber a user's own `~/.tmux.conf` — scope it to Dejima sessions.
- Verify passthrough + extended-keys actually take effect on a Dejima-created session.

## Deliverable 2 — host→island image-paste bridge

Capture an image paste **host-side** → write it to the island's **intake dir** → inject a
`Read <path>` reference so the agent picks it up.

- Reuse the existing **intake dir** mechanism (Port intake; see `internal/` Port/intake
  code + `docs/port-island-spec.md`). Don't invent a new channel.
- Host-side capture is platform-specific (macOS `pbpaste`/clipboard image). Keep it behind
  a clean seam; degrade gracefully where unsupported.
- The injection writes the image into the target island's intake and surfaces a
  `Read <path>` line to the agent's input.

**You own:** a committed `tmux.conf` + the session-create wiring that references it; a new
paste-bridge module + its CLI/keybinding entry; tests. **Do NOT touch:** install/uninstall,
`internal/api/` grant routes, the uninstall block. Coordinate only at the tmux/session seam.

**Workflow:** Own worktree, branch `feat/p1-paste-tmux`. Never `cd /workspace` or enter
another worktree. `go test ./...` + `golangci-lint run` (v2; master requires lint+build).
Commit only your own hunks; PR to `master` when green. Go 1.26.3. **If Deliverable 2 proves
large or platform-blocked, ship Deliverable 1 as its own PR first** — don't let the bridge
hold the tmux fix.

**Done when:** Dejima sessions run with `allow-passthrough on` + extended-keys (Shift+Enter
+ notifications work through tmux); and a host image paste lands in the island intake with a
`Read <path>` injected — or, if the bridge is blocked, the tmux.conf ships alone with the
bridge scoped/handed off in the PR.
