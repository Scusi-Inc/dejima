# Applying secrets to running agents — design

**Status:** Phase 1 + 2 shipped (`feat/secrets-restart-tui`) — recreate-to-apply,
graceful per-agent restart with resume, and the restart checklist.

## The mechanical model (why this needs care)

Island secrets are **container-level**: one `secrets.env` file, bind-mounted at
`/opt/host/secrets.env`, shared by every agent in the island. A login shell
sources it once (via `/etc/profile.d`). Two consequences drive everything:

1. **A running process keeps its launch-time environment.** Detaching/reattaching
   the client (closing/opening a terminal) does nothing — the agent process is
   still running with the env it started with. To pick up a new secret, the agent
   process must **relaunch** in a fresh login shell.
2. **The mount is decided at container-create time.** An island created before it
   had any secret has no mount, and a mount can't be added to a live container —
   so the FIRST secret needs a container **recreate** (`upgrade`) to gain the
   mount. (The always-mount fix makes new islands carry the mount from creation;
   older ones still need one recreate.)

So "which agents to restart" only becomes a meaningful *choice* once the mount
already exists (2nd+ secret). Before that, it's all-or-nothing: a whole-container
recreate that relaunches every agent together.

## Phase 1 — shipped

The Secrets pane now:
- states the real mechanism (recreate to apply; reopening a terminal won't), and
- offers **[R] recreate to apply** → the existing `recreate-island` confirm (its
  own second warning: "Running agent sessions restart; workspace + state
  preserved"), which recreates the container and relaunches agents with the new
  env.

## Phase 2 — proposed

### A. Graceful resume across the restart
A recreate today ends the running conversation. Relaunch agents in a way that
**resumes** it:
- `claude-code` → launch with `claude --continue` (resume the latest session in
  the worktree); `codex` → its own resume flag; `shell`/`headless` → no-op.
- Implementation: an optional "resume" mode on `ensureAgentSession` /
  `agentLaunchScript`, keyed on the handler (a new `ResumeLaunch` field, or a
  per-handler resume arg), used when a restart is *operator-initiated* (secret
  apply, OOM apply) rather than a crash. Off by default; opt-in per restart.

### B. Per-agent / terminal restart checklist
When the mount already exists, a full container recreate is heavier than needed —
relaunching just the affected agent processes suffices. Offer a checklist:
- List the island's agents + terminals; multi-select with **Enter**; a
  **select/deselect-all** toggle; restart only the chosen ones.
- Needs **per-agent restart plumbing**: kill the agent's tmux session and
  re-run `ensureAgentSession` (which re-sources the login shell → new env). This
  does NOT exist yet as an operator verb (only whole-container recreate does).
- Gate: the checklist is offered only when the secrets mount is already present.
  For the first secret (no mount), fall back to the whole-island recreate — a
  checklist there would be a lie, since the mount can only be added by recreate.

### Open questions
- Resume UX when the worktree changed underneath the agent (Claude's `--continue`
  resumes the transcript, not the process state) — acceptable, but worth a note.
- Whether per-agent relaunch should also apply to a *rotated* (changed) secret,
  not just an added one — same mechanism; the pane's `restartPending` already
  fires on any change.
