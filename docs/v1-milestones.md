# Dejima v1 — Milestone Plan

**Status:** Draft
**Last updated:** 2026-05-21

This is the build plan for Dejima v1. Each milestone is sized for one to three Claude Code sessions, leaves the repo in a buildable and testable state, and has acceptance criteria expressed as runnable checks where possible.

The plan front-loads the architecture (daemon from day one) to avoid mid-project rewrites. The final milestone is dogfood — moving real Claude Code usage onto Dejima as the working definition of "v1 done."

---

## M0 — Foundation

**Goal:** A buildable Go repo with the structure the spec describes, CI, and the docs already committed (this conversation).

**Scope**
- Go module init.
- Directory skeleton matching `cmd/`, `internal/`, `image/`, `docs/`.
- Empty stub `cmd/dejima/main.go` and `cmd/dejimad/main.go` that print version and exit.
- `Makefile` or `justfile` with `build`, `test`, `lint`, `fmt` targets.
- GitHub Actions: `go build ./...`, `go test ./...`, `golangci-lint` on macOS and Linux runners.
- `.gitignore`, `.editorconfig`.

**Out of scope**
- License (deferred per spec §11.2). `LICENSE` placeholder noting "TBD before public release."
- Any real CLI behavior.

**Acceptance**
- `go build ./...` succeeds on macOS and Linux.
- `go test ./...` succeeds (no tests yet, exit 0).
- `dejima --version` prints something.
- CI green on a push.

---

## M1 — MVP island + daemon

The biggest milestone. Ships an end-to-end vertical slice: create an island, connect to it, run Claude Code, have it push to GitHub.

**Goal:** From a fresh Mac mini install, the user can `dejima init` against a real GitHub repo, `dejima connect`, run Claude Code inside the island, have the agent make a commit, and see the push land on GitHub.

**Scope**
- `dejimad` daemon: HTTP-over-Unix-socket; JSON-over-HTTP.
- API endpoints: `POST /v1/islands`, `GET /v1/islands`, `GET /v1/islands/:name`, `DELETE /v1/islands/:name`.
- `dejima` CLI verbs: `init`, `connect`, `ls`, `status`, `purge`.
- `image/Dockerfile` for the island image:
  - Ubuntu LTS base.
  - `git`, `tmux`, `curl`, `ca-certificates`, `openssh-client`.
  - `gh` (GitHub CLI).
  - Claude Code installed.
  - A non-root user (`dejima`) with a writable `/workspace` mount.
  - `tmux.conf` baked in with sensible defaults.
- `image/agents/claude-code/` shim directory:
  - A canned `CLAUDE.md` template informing Claude Code about the island environment.
  - Layered into the workspace at init when `--agent claude-code` (the default).
- `dejima init`:
  - Creates per-project config and named Docker volume.
  - Mounts host `~/.config/gh`, `~/.claude` read-only into the island.
  - Inherits host's `git config user.name`/`user.email`.
  - Clones the target repo into the volume.
  - Runs `gh auth setup-git` inside the island.
  - Starts the container with a long-lived tmux session running the agent (Claude Code by default).
- `dejima connect` opens an interactive PTY into the tmux session via `docker exec -it` initially (multi-attach via API comes in M3).
- `dejima purge` stops the container and removes the volumes after a confirmation prompt.

**Out of scope**
- Hibernate/wake (M2).
- Multi-attach via the API (M3).
- Service install (M4).
- Remote access (M4).
- Resource caps (M5).

**Acceptance**
- Tester: pick a small GitHub repo where the user has push access.
- `dejima init --repo <url>` succeeds; container is running; volumes exist.
- `dejima ls` shows the island.
- `dejima connect` lands the user inside Claude Code's prompt.
- Inside, ask Claude Code to make a trivial commit and push. Push succeeds; commit appears on GitHub with the host user's name/email.
- Disconnect; `dejima ls` still shows the island; reconnect; tmux session is intact.
- `dejima purge <name>` removes everything.

---

## M2 — Persistence + lifecycle

**Goal:** Hibernate, wake, and reset work cleanly. Host reboots don't lose islands.

**Scope**
- `POST /v1/islands/:name/hibernate` and `POST /v1/islands/:name/wake` endpoints.
- `POST /v1/islands/:name/reset` endpoint.
- `dejima hibernate <name>` stops the container, preserves volumes.
- `dejima wake <name>` starts the container against existing volumes; brings up a fresh tmux session and re-launches the agent.
- `dejima reset <name>` clears the agent on-disk state volume (e.g., `.claude/`) while preserving the workspace volume. Confirms before acting.
- Daemon at startup re-adopts existing islands: any container that should be running gets started.
- `dejima status <name>` reports lifecycle state (running, hibernated).
- Per-project config persists desired state (so daemon knows whether to wake on boot).

**Out of scope**
- Auto-hibernate after idle (post-v1).
- Multi-attach API (M3).

**Acceptance**
- Initialize an island; have Claude Code start a conversation; observe `.claude/` state on the volume.
- `dejima hibernate <name>` — container stops, status reflects it.
- Restart `dejimad` (or reboot host) — daemon re-adopts; status still shows hibernated.
- `dejima wake <name>` — container restarts; agent state is preserved; chat history visible to the new Claude Code process.
- `dejima reset <name>` — agent state cleared; workspace files intact; on next connect, Claude Code starts a fresh conversation but `git log` and edits are untouched.

---

## M3 — Multi-attach session via the API (with presence)

**Goal:** The Dejima API serves PTY streams. Multiple clients can attach simultaneously and see who else is attached.

**Scope**
- `GET /v1/islands/:name/session` returns a websocket carrying a multiplexed PTY stream.
- Server-side bridges the websocket to the in-container tmux session (`tmux attach-session -t dejima`).
- N concurrent clients are supported; tmux's native multi-attach provides shared-screen semantics.
- **Presence**: each connection carries a `?label=<name>` query parameter (e.g., `laptop`, `phone-pwa`). On attach, the API returns the list of currently-attached clients with labels and last-seen timestamps. The CLI prints "Also attached: phone-pwa (10s ago)" on connect.
- `dejima connect` updated to use the API instead of `docker exec`. Defaults its label to a hostname-derived value; override with `--as <label>`.
- Disconnect handling: closing the websocket detaches that client from tmux; other clients keep going. Presence list updates accordingly.

**Out of scope**
- Mobile/web client implementations (post-v1 in this repo; example client elsewhere).
- Session check-in/check-out lock semantics (post-v1).

**Acceptance**
- Open two terminals on the host. From each, `dejima connect <name> --as laptop` and `--as desktop`.
- Both see the same screen. Either can type; both see the input. Each printed the other's presence on attach.
- Disconnect one terminal. The other keeps working uninterrupted; subsequent `dejima status` shows only the remaining client.
- Reconnect from a third terminal. Sees current state and the live presence list.
- Manual test of websocket from `wscat` or similar to validate the API independent of the CLI.

---

## M4 — Service install + remote access + webhooks

**Goal:** Daemon runs as a launchd service on macOS; reachable from another Tailscale device. State-change events flow out via webhooks; the Claude Code shim emits agent-specific events.

**Scope**
- `dejima service install` writes the launchd plist (macOS) or systemd user unit (Linux), enables, and starts it.
- `dejima service uninstall` reverses.
- `dejima daemon --foreground` runs the daemon in dev mode (mutually exclusive with the service).
- Daemon listens on:
  - Unix socket at `~/.dejima/dejimad.sock` (always).
  - TCP port (configurable, default e.g. `:7273`) bound to Tailscale-pinned IPs.
- Daemon refuses TCP connections from non-tailnet IPs (lookup via `tailscale status` or interface inspection).
- `DEJIMA_HOST` env var routes the CLI: unset → Unix socket; set to `host:port` → remote daemon.
- **Webhooks**:
  - `POST /v1/events/subscribe { url, secret?, events? }` registers a webhook.
  - Daemon POSTs JSON to the URL on state changes. Payload includes event type, island name, timestamp, and a signed `X-Dejima-Signature` header if a secret was provided.
  - Daemon-observable events emitted: `island.created`, `island.running`, `island.hibernated`, `island.woken`, `island.reset`, `island.purged`, `container.crashed`, `client.attached`, `client.detached`, `last-client.detached`.
- **Agent-event endpoint**:
  - `POST /v1/internal/agent-event { island, type, payload }` — used by per-agent shims to emit agent-specific events (e.g., `agent.waiting-for-input`, `agent.task-complete`). These also propagate to webhook subscribers.
  - Claude Code hook (in `image/agents/claude-code/`) installed in M1's image is wired up here.

**Out of scope**
- SSH-only mode, token-auth mode (post-v1).
- Multi-host CLI (`dejima --host` flag).
- Chat-service integrations (Slack/WhatsApp/SMS) — these are external apps that consume the webhook + session API; they don't live in this repo.

**Acceptance**
- `dejima service install` on Mac mini; reboot Mac mini; daemon comes back up automatically; islands resume.
- From a laptop on the same tailnet: `DEJIMA_HOST=mac-mini.tailnet:7273 dejima ls` succeeds.
- From an off-tailnet machine: connection is refused.
- `dejima daemon --foreground` runs the daemon in the terminal for debugging.
- Subscribe a webhook to a local listener (e.g. `webhook.site` or `nc -l`). `dejima hibernate <name>` triggers an `island.hibernated` POST; `dejima wake` triggers `island.woken`.
- With Claude Code running, an `agent.waiting-for-input` event arrives at the webhook within a few seconds of Claude prompting.

---

## M5 — Resource caps + lightweight access + multi-agent polish

**Goal:** Per-project resource configuration; multi-agent on the same repo is ergonomic; lightweight access verbs work without claiming the session.

**Scope**
- `[resources]` section parsed from per-project config: `memory`, `cpus`, `disk`.
- `dejima init --memory 4G --cpus 2` flags map onto the config.
- Resource settings translate to `docker run --memory --cpus --storage-opt`.
- Multi-agent disambiguation: when init's auto-derived name collides, the CLI errors with a useful message; `dejima init --name foo-codex --agent codex` handles it.
- `dejima ls` shows `(name, repo, agent, status, last-touched)` clearly.
- **Lightweight access verbs**:
  - `dejima exec <name> -- <cmd>` — run a one-shot command, stream stdout/stderr, return exit code. No session attach.
  - `dejima cp <name>:<path> <local>` and `dejima cp <local> <name>:<path>` — copy files in or out of the island.
  - `dejima logs <name>` (with `--follow`) — tail container logs.
- Error messages reviewed; `dejima status` output is useful.

**Out of scope**
- Quota enforcement beyond what Docker provides.
- Proactive monitoring / alerting.

**Acceptance**
- Create two islands on the same repo with different agents; both run; both visible in `ls`.
- Set a memory cap; verify the container is limited (use `docker inspect`).
- `dejima exec <name> -- ls /workspace` returns the workspace listing without disturbing the active session.
- `dejima cp <name>:/workspace/README.md ./` copies a file out.
- `dejima logs <name> --follow` streams container output as the agent works.
- Trigger a few common error paths (bad repo URL, duplicate name, daemon down) and confirm errors are intelligible.

---

## M6 — Dogfood

**Goal:** User moves daily Claude Code work onto Dejima on the Mac mini.

**Scope**
- README install instructions for Mac mini (one-paragraph happy path).
- A short troubleshooting section based on what came up during M1–M5.
- Smoke tests run on a clean install.
- User uses Dejima as their primary Claude Code setup for one full week.
- Issues filed for any rough edges; trivial ones fixed inline; larger ones become post-v1 candidates.

**Out of scope**
- Public release (license, repo visibility, etc.) — separate decision after dogfood feedback.

**Acceptance**
- User reports it's working as a daily driver.
- A meta-test: `dejima init` against the dejima repo itself; develop the next milestone inside an island. If you can't dogfood the project inside itself, something is wrong.

---

## Notes on working method

- Each milestone leaves the repo in a buildable and testable state. No half-finished refactors carried across sessions.
- Tests matter more than usual: the next session uses them to verify what was claimed to be done.
- Acceptance criteria are written before implementation in each milestone. Done = criteria met.
- Trivial milestones (M0, M6) might be one session. M1 is plausibly three. The rest are one to two each.
