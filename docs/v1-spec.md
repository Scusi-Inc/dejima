# Dejima v1 — Specification & Decision Record

**Status:** Draft
**Last updated:** 2026-05-21

This document captures the decisions made during v1 planning and flags what remains open. It is a living document.

---

## 1. Positioning

Dejima is an **interstitial OSS layer** between AI coding agents and the host machine they run on. It is the contained box those agents live inside, plus the lifecycle and access plumbing around it.

It is explicitly **not the interface to the agent**. That stays with whatever CLI/SDK the agent itself ships (Claude Code, Codex CLI, etc.). Dejima is a *membrane layer* meant to be consumed by other applications.

The CLI is a convenience surface — the canonical "user" is another piece of software.

### One-line pitch

> *"The substrate for multi-device agent workflows."*

### Differentiator

The sandbox space is crowded (E2B, Vercel Sandbox, Daytona, container-use, DevPod, Coder, Modal Sandboxes). Dejima's wedge is **opinionated agent infrastructure with first-class multi-access-point sessions, agent-agnostic and self-hosted by design** — not novel isolation tech.

## 2. Audience

- **Primary:** developers running coding agents on their own infrastructure (Mac mini, home server, VPS, cloud VM).
- **Single-user**, but with **multi-access-point sessions**: one user, many devices. Drive a session from a laptop, hand off to a phone, switch back to the desktop. Not multi-user concurrency.
- **Other apps as first-class clients**: mobile apps, web UIs, IDE plugins, third-party tools (e.g., an "agent manager" that orchestrates Dejima instances across multiple hosts).
- **Aspirational:** small teams in a future release.
- **Not the audience:** enterprise with SOC2/compliance/RBAC needs.

## 3. Agent Targets (priority order)

1. **Claude Code** — primary first-class target.
2. **Codex CLI** — secondary; same container shape, different binary.
3. **Any CLI agent** — black-box contract: "Linux shell + workspace + persistent session + scoped secrets."
4. **API / headless agents** — deferred to post-v1 but accommodated in the API design.

**Defensibility principle:** every feature must work for any agent. No code paths conditional on agent identity. The vendor-agnostic position is the structural defense against Anthropic/OpenAI absorbing this feature for their own agents.

### Per-agent shims

A clean architectural split keeps the agnostic claim honest:

- **Core (strictly agnostic)**: the daemon, the API, the runtime layer, the bridge. No `if agent == "x"` anywhere. The agent is a command that runs in a shell.
- **Shims (per-agent, opt-in)**: optional helpers that improve the experience for a specific agent without changing the core contract. Live under `image/agents/<agent>/` in the source repo and are layered into the island at init based on `--agent`.

Examples of shims:
- A canned `CLAUDE.md` informing Claude Code about its environment (workspace is `/workspace`, `gh` is available, etc.).
- A Claude Code hook that POSTs to `dejimad`'s `/v1/internal/agent-event` endpoint when the agent waits for input or finishes a task — feeds presence/notification UX without the daemon needing to parse terminal output.
- A pre-configured `.claude/settings.json` with sensible defaults for the contained environment.
- (Future) Codex shims, Aider shims, etc.

v1 ships Claude Code shims because that's the dogfood path. Codex shims as Codex usage grows. The core never assumes any shim exists; the shims are dead weight if you swap agents and that's fine.

## 4. Host Targets

| Priority | Host                              | Backend                                    |
|----------|-----------------------------------|--------------------------------------------|
| P0       | macOS on Mac mini                  | Docker Desktop / OrbStack / colima         |
| P1       | Linux home server / VPS / laptop   | Docker or Podman                           |
| P2       | Cloud VM (any provider)            | Docker or Podman                           |
| Later    | Apple's native `container` CLI     | Apple Virtualization framework             |
| Later    | Firecracker microVM                | Real microVM isolation (E2B-style)         |

Target the Docker API as the lowest common denominator for v1. All P0–P2 hosts support it.

## 5. v1 Scope

### In

- **Host daemon (`dejimad`)** managing islands and exposing the Dejima API.
- **Installed as a launchd service (macOS) or systemd user unit (Linux)** so it survives reboots.
- **CLI (`dejima`)** as a thin client of the API: `init`, `connect`, `ls`, `status`, `hibernate`, `wake`, `import`, `export`, `purge`, `service`.
- **One container per island**, hosting one repo and one agent. Each island is a `(repo, agent, name)` tuple.
- **Always-on containers by default**, with first-class `hibernate`/`wake` lifecycle for memory pressure relief.
- **Persistent volumes** for workspace and agent on-disk state, surviving container restart and host reboot. `purge` is the only destructive operation.
- **Multi-attach sessions**: multiple clients see the same screen simultaneously (tmux semantics).
- **Direct push to GitHub from inside the island**: host credentials mounted read-only; GitHub stays canonical.
- **Agent auth via mounted host credentials** (`~/.claude`, `~/.codex` read-only).
- **Tailscale-pinned remote API** for off-host clients. Local clients use Unix socket.
- **Per-project resource caps (configurable, default unlimited)** mapping to Docker's `--memory`/`--cpus`/`--storage-opt`.
- **Presence-aware sessions**: when a client attaches, it learns who else is attached.
- **`dejima reset`** — clear agent on-disk state without destroying the workspace.
- **Per-agent shims**: opt-in, agent-specific helpers (CLAUDE.md template, status hooks) that improve the dogfood experience without contaminating the agnostic core. Claude Code shims ship in v1.
- **Lightweight access verbs**: `dejima cp`, `dejima exec`, `dejima logs --follow` for scripts and observability without claiming the session.
- **Webhook notifications**: daemon POSTs state-change events to a configured URL.

### Out (deferred to post-v1)

- Audit log / Ledger.
- Fine-grained network egress allow-list (default: open egress).
- Multi-user / RBAC.
- Cross-host orchestration via the CLI (each daemon stays independent; clients orchestrate multi-host).
- Concurrent agents inside a single island.
- Multiple repos per island.
- MCP server brokering between host and island.
- SSH-only / token-auth modes for the daemon (Tailscale only for v1).
- Hosted/SaaS variant.

## 6. Architecture

### Filesystem layout (host)

```
~/.dejima/
  config.toml              # host-level config: daemon address, defaults
  dejimad.sock             # daemon Unix socket
  projects/<name>/
    config.toml            # project metadata: repo, agent, resources, name
    intake/                # bind-mounted to /intake in island (for `dejima import`)
    exports/               # destination for `dejima export` artifacts
    logs/                  # daemon-captured container stdout/stderr
```

Workspace and agent on-disk state live in **named Docker volumes**, not under `~/.dejima/`. Containers are cattle; volumes are pets.

### Process model

```
launchd / systemd
    └── dejimad         # the host daemon
            ├── manages island containers via the Docker API
            ├── serves the Dejima API on:
            │     - Unix socket (~/.dejima/dejimad.sock) — local clients
            │     - Tailscale-pinned TCP port — remote clients
            └── mediates client attach/detach (PTY multiplexing)

dejima                  # the CLI (a client of the API)
```

### Source repo layout

```
cmd/
  dejima/                # CLI entrypoint
  dejimad/               # daemon entrypoint
internal/
  api/                   # API types, protocol, server, client
  runtime/               # backend abstractions (docker, future: podman, firecracker)
  project/               # project lifecycle, config
  bridge/                # PTY multiplexing, session I/O
  service/               # launchd/systemd unit generation + install
image/
  Dockerfile             # the canonical island image
  agents/
    claude-code/         # optional Claude Code shims (CLAUDE.md template, hooks, defaults)
    codex/               # (future) Codex CLI shims
docs/
  v1-spec.md             # this document
  v1-milestones.md       # build plan
```

### The Dejima API (concept)

JSON-over-HTTP with websocket streams for PTY. Endpoints (v1):

- `GET    /v1/islands`                    — list
- `POST   /v1/islands`                    — create
- `GET    /v1/islands/:name`              — status
- `DELETE /v1/islands/:name`              — destroy (purge)
- `POST   /v1/islands/:name/hibernate`    — stop container, preserve volumes
- `POST   /v1/islands/:name/wake`         — start container against existing volumes
- `POST   /v1/islands/:name/import`       — push files in
- `POST   /v1/islands/:name/export`       — pull a diff/patch out
- `POST   /v1/islands/:name/reset`        — clear agent on-disk state, preserve workspace
- `GET    /v1/islands/:name/session`      — websocket PTY stream; multiple clients can hold simultaneously
- `POST   /v1/islands/:name/exec`         — run a one-shot command; stream stdout/stderr
- `GET    /v1/islands/:name/files/*path`  — read a file from the island
- `PUT    /v1/islands/:name/files/*path`  — write a file into the island
- `GET    /v1/islands/:name/logs`         — stream/tail container logs (supports `?follow=true`)
- `POST   /v1/events/subscribe`           — register a webhook URL for state-change events
- `POST   /v1/internal/agent-event`       — internal endpoint used by per-agent shims to emit agent-specific events (not for external use)

The CLI is the first consumer. The contract is the public surface; third-party apps target the API, not the CLI.

### Bridge (v1)

Inside the container, "the bridge" is a long-lived tmux session attached to the agent process. The daemon's PTY endpoint multiplexes that tmux session to N connected clients. No custom in-container daemon is required for v1; if/when API-agent paths mature, a small in-container helper (`dejima-agent`) can be added without changing the public API.

### Multi-access-point session model

- Each island has exactly one logical session.
- Backed by a single tmux session inside the container.
- N clients can be attached at once; all see the same screen, any can send input.
- **Presence**: on attach, the API tells the client who else is currently attached (client label, last-seen timestamp). No check-in/check-out lock semantics — just visibility.
- Disconnects are non-events: tmux keeps state; clients reconnect to where they left off.
- This is *not* "session handoff with check-in/check-out" — that richer feature is deferred unless real demand emerges. Shared-tmux semantics cover the stated use case.

## 7. Lifecycle & Persistence

### Container lifecycle

| Verb              | Effect                                                                        |
|-------------------|-------------------------------------------------------------------------------|
| `dejima init`     | Create island: volume + container + agent process. Container starts running. |
| `dejima connect`  | Open a PTY stream to the island's tmux session.                              |
| `dejima hibernate`| Stop container gracefully. Volumes preserved. Use when memory leaks bite.    |
| `dejima wake`     | Start container against existing volumes. Fresh tmux/agent process.          |
| `dejima reset`    | Clear agent on-disk state (chat history, scratch files). Workspace preserved.|
| `dejima purge`    | **Destroy** container and volumes. Only destructive verb.                    |

Containers also auto-start when the daemon boots, so host reboots don't lose islands. Optional auto-hibernate after N hours idle is opt-in.

### What survives what

| State                          | Disconnect | Container restart | Host reboot | Hibernate/Wake | Reset | Purge |
|--------------------------------|:----------:|:-----------------:|:-----------:|:--------------:|:-----:|:-----:|
| Workspace files (`/workspace`) |     ✅     |        ✅         |     ✅      |       ✅       |   ✅  |   ❌  |
| Agent on-disk state            |     ✅     |        ✅         |     ✅      |       ✅       |   ❌  |   ❌  |
| tmux/PTY session               |     ✅     |        ❌         |     ❌      |       ❌       |   ❌  |   ❌  |
| Running agent process          |     ✅     |        ❌         |     ❌      |       ❌       |   ❌  |   ❌  |
| In-memory agent state          |     ✅     |        ❌         |     ❌      |       ❌       |   ❌  |   ❌  |

On wake, the agent restarts fresh but reads its own persisted state (chat history, scratch files). Conversational continuity depends on the agent's own behavior, not on Dejima keeping a PTY warm.

## 8. Authentication & Credentials

The agent fundamentally needs *some* credential to call its LLM API. You can't isolate the agent from the credential that lets it work — that's the agent's job. Dejima isolates everything *else* (host filesystem, other projects' creds, host secrets).

### Agent auth (LLM provider)

- **Mount host's agent credential dir read-only** into the island (e.g., `~/.claude`, `~/.codex`).
- User logs in once on host; all islands share the same OAuth credentials.
- No browser inside the container; no OAuth tunneling code in v1.
- Threat model documented: your agent has your LLM credentials by definition; Dejima isolates everything else.

### Git auth (push to GitHub)

- **Mount `~/.config/gh/` read-only** into the island; **pre-install `gh` in the image**.
- At island init, run `gh auth setup-git` to wire `git push` through the GitHub CLI's credential helper. Works for both HTTPS and SSH-style remotes.
- Bonus: `gh` is available to the agent for `gh pr create`, `gh issue list`, etc.
- **Opt-in alternative**: `dejima init --ssh-key ~/.ssh/id_ed25519_github` mounts a specific SSH key for users who don't use `gh`.
- **Explicitly not supported**: bulk-mounting `~/.ssh`. Other host identities (production servers, etc.) should not be exposed to the island.

### Git commit attribution

- **Inherit host's `git config user.name` / `user.email`** into each island at init.
- Commits look like the user made them. Matches "agent acts on my behalf" mental model.
- Configurable per-project if a user wants synthetic attribution.

### Daemon auth (remote API)

- **Tailscale-pinned** for v1: daemon binds to tailnet IPs only; identity = Tailscale identity.
- **Local clients** authenticate via filesystem permissions on the Unix socket.
- **Roadmap**: SSH-tunnel-only mode, pre-shared token mode — added when demand emerges.

## 9. CLI Surface

### Verbs

| Verb        | Purpose                                                              |
|-------------|----------------------------------------------------------------------|
| `init`      | Create an island.                                                    |
| `connect`   | Attach to an island's session.                                       |
| `ls`        | List all islands and their status.                                   |
| `status`    | Detail view of a single island.                                      |
| `hibernate` | Stop container, preserve volumes.                                    |
| `wake`      | Restart container against existing volumes.                          |
| `reset`     | Clear agent on-disk state. Preserves workspace.                      |
| `import`    | Send a file/dir into the island's `/intake/`.                        |
| `export`    | Pull a diff/patch out (secondary path; primary is `git push`).       |
| `cp`        | Copy a file in or out of the island. Scriptable; no session attach.  |
| `exec`      | Run a one-shot command inside the island; stream stdout/stderr.       |
| `logs`      | Tail island logs (`--follow`). Observability without claiming session.|
| `purge`     | Destroy island and volumes. Confirms before acting.                  |
| `service`   | Install/uninstall the daemon as a launchd/systemd unit.              |
| `daemon`    | Run the daemon in foreground (dev/debugging).                        |

### Project handles — the `(repo, agent, name)` model

- An island is identified by **name** (unique per host).
- Name **defaults to the repo directory basename** (`~/code/foo` → `foo`).
- If the default name collides, init errors and prompts for `--name`.
- Explicit form: `dejima init --name foo-codex --agent codex --repo ~/code/foo`.
- The CLI **remembers the most-recently-touched island**; verbs without a name argument default to it.
- `dejima ls` shows `(name, repo, agent, status, last-touched)` so multi-agent setups are legible at a glance.

### Resource configuration

- Per-project `[resources]` block in `~/.dejima/projects/<name>/config.toml`:
  ```toml
  [resources]
  memory = "4G"     # docker --memory
  cpus   = "2.0"    # docker --cpus
  disk   = "20G"    # docker --storage-opt
  ```
- All keys optional. Unset = unlimited.
- Convenience flags at init: `dejima init --memory 4G --cpus 2`.

### Binary

- Ship `dejima` only.
- Document `alias dj=dejima` in the README for users who want the short form.
- Single canonical name keeps brand and docs focused.

## 10. Concept Vocabulary

| Term     | Meaning                                                       |
|----------|---------------------------------------------------------------|
| Island   | The container holding a single project and a single agent.   |
| Bridge   | The brokered I/O channel between host and island.            |
| Trade    | A synced export of changes from island to host.              |
| Intake   | Files passed into the island via `dejima import`.            |
| Ledger   | (Post-v1) An audit log of every bridge transaction.          |

CLI verbs are intentionally functional; the metaphor lives in concept names, status output, logs, and documentation.

## 11. Open Questions

Most of v1's structural decisions are resolved. What remains:

### 11.1 Island image shape

Whether v1 ships one image or a `lean` / `full` split. Starting strawman: Debian/Ubuntu base + `git` + `tmux` + `curl` + `ca-certificates` + `gh` + Claude Code + Codex CLI. Add toolchains only when a real test demands it. Decide after the dogfood phase reveals actual gaps.

### 11.2 OSS license

Not chosen. To decide before public release. Apache 2.0 is the strawman.

### 11.3 API protocol details

JSON-over-HTTP is the v1 default; websocket for PTY streams. Wire format details (envelope schema, error model, versioning headers) settled during implementation.

## 12. Adjacent Tools (reference)

For context. None is a direct competitor; each informs design choices.

- **E2B**, **Vercel Sandbox**, **Modal Sandboxes** — hosted Firecracker sandboxes for agents.
- **container-use** (Dagger) — closest direct analogue; agent-per-container with diff-out.
- **DevPod**, **Daytona**, **Coder**, **Gitpod self-hosted** — remote dev environments.
- **Devin**, **Replit Agent**, **OpenAI Codex Cloud**, **Cursor background agents** — hosted agent platforms.
- **Claude Code remote mode** — Anthropic's own multi-device session story for Claude Code; Dejima occupies adjacent ground but at the membrane layer rather than the agent layer.

## 13. Implementation Plan

See [`v1-milestones.md`](v1-milestones.md) for the milestone-by-milestone build plan, sized for Claude Code sessions.
