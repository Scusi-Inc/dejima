# Agent adapters

This doc describes the contract for adding an agent to Dejima. It covers two
paths:

1. **Use the built-in `headless` agent type** — for any process that takes a
   command line. No image rebuild needed; you just pass `--cmd "…"` at
   `dejima init`. Works for Python SDK loops, custom Node scripts, aider, any
   binary you can `npm install`/`pip install` once and invoke. **Start here.**
2. **Add a first-class agent type** — for agents that need credential injection,
   a template config, or pre-installed dependencies that should be baked into
   the island image. This requires a small `init.sh` shim under
   `image/agents/<name>/` and a one-line entry in `image/start.sh`.

Both paths get the same substrate: per-project workspace volume, GitHub
credentials, lifecycle events, the `agent.*` event endpoint, multi-device API
control. The only difference is whether you ship a shim with the image.

---

## Path 1: Headless agent (no image changes)

Headless islands run your command as the container's main process. No tmux,
no attach surface — `dejima connect` returns a 409 with a pointer to
`dejima logs`. Stdout/stderr are captured by Docker and reachable through:

```bash
dejima logs <island>            # snapshot
dejima logs <island> --follow   # tail
```

Create one with:

```bash
dejima init --repo <repo> --agent headless \
  --cmd "python -m my_agent.loop"
```

The command is persisted on the project so `dejima reset` reprovisions with
the same entrypoint. When your command exits, the container exits — no
restart-on-crash supervisor (yet); use `dejima wake <name>` to relaunch.

### Environment your command runs under

| Var | Provided | Notes |
|-----|----------|-------|
| `DEJIMA_PROJECT_NAME` | always | The island's name. |
| `DEJIMA_AGENT` | always | `headless` for this path. |
| `DEJIMA_AGENT_CMD` | always | Your `--cmd` value. |
| `DEJIMA_SOCKET` | always | Unix socket path for the daemon's internal API (see "Emitting events"). Bind-mounted from the host only on native-Linux deployments — your shim must no-op when the socket isn't present. |
| `HOME` | always | `/home/dejima`. |
| `GH_CONFIG_DIR` | always | Read-only mount of the host's `gh` config so `git push` works. |
| Working directory | `/workspace` | Your repo is cloned here on first boot. |

### Persistent state

Three volumes survive container restarts:

- `/workspace` — your repo + working files. Edits persist.
- `~/.agent-state` — a per-agent state dir for headless agents (CLI agents
  use `~/.claude` and `~/.codex` instead). Use this for caches, learned
  weights, conversation history — anything you want preserved across
  hibernate/wake.
- The on-disk project record under `~/.dejima/projects/<name>/` is host-side,
  not container-visible, and holds intake/exports/logs subdirs.

### Emitting events

POST to the daemon's Unix socket from inside the island:

```bash
curl --unix-socket "$DEJIMA_SOCKET" \
  -H "Content-Type: application/json" \
  -X POST http://dejimad/v1/internal/agent-event \
  -d '{"island":"'"$DEJIMA_PROJECT_NAME"'","type":"agent.task-complete"}'
```

Three event types are surfaced into `IslandInfo.AgentState` (and shown in the
TUI's `!` glyph + detail pane):

- `agent.waiting-for-input` — agent paused, expects human input.
- `agent.task-complete` — agent finished its current loop iteration.
- `agent.error` — agent hit an unrecoverable error.

Other types pass through to webhook subscribers but won't update
`AgentState`. Use the `payload` field freely; it's stored on the event but
not interpreted by the daemon.

---

## Path 2: First-class agent type (image change)

Add this when:

- The agent needs credential injection from the host (`~/.claude`,
  `~/.codex`, etc.).
- It ships a template config file that should be dropped into `/workspace`
  on first boot.
- It needs notification hooks wired into its own config format.

### The shim contract

A directory at `image/agents/<name>/` is automatically picked up by
`start.sh`. Only one file is required:

```
image/agents/<name>/init.sh    # executable; run on every container boot
```

`init.sh` runs as the `dejima` user with `set -euo pipefail`. Idempotency
matters — the container may restart, and the shim runs each time.

Optional companions (mirroring the Claude Code and Codex shims):

```
image/agents/<name>/<TEMPLATE>           # e.g. CLAUDE.md / AGENTS.md
image/agents/<name>/hooks/notify.sh      # event-emitting helper
```

`init.sh`'s responsibilities, in order, are:

1. Copy host-mounted credentials into the agent's writable config dir
   (typical pattern: read-only mount at `/opt/host/<agent>/`, copy what's
   needed into `~/.<agent>/`).
2. Drop the template file into `/workspace` if missing.
3. Install any notification hooks the agent supports, and wire them up via
   the agent's config file (without clobbering anything the user already
   has there).

See `image/agents/claude-code/init.sh` and `image/agents/codex/init.sh` for
worked examples.

### Wiring the command

Add the agent's launch command to the `case` block in `image/start.sh`:

```sh
case "$AGENT" in
    claude-code) AGENT_CMD="claude" ;;
    codex)       AGENT_CMD="codex --sandbox-policy=no-sandbox" ;;
    your-agent)  AGENT_CMD="your-binary --your-flags" ;;
    headless)    # … unchanged
    *)           AGENT_CMD="${AGENT}" ;;
esac
```

CLI agents run inside the tmux session that backs `dejima connect`. If your
agent doesn't have a terminal UI, prefer Path 1 (headless) over adding a new
type.

### Installing the agent binary

Add an install step to `image/Dockerfile`, after the `USER dejima` switch
(so the bin lands in the user-owned `NPM_CONFIG_PREFIX`):

```dockerfile
RUN npm install -g <your-package>
# or: RUN pip install --user <your-package>
# or: download a release binary into /home/dejima/.local/bin/
```

---

## Worked example: a minimal headless adapter (no shim)

A 10-line Python loop that polls a task queue, runs work, and signals
completion via the agent-event endpoint:

```python
# my_loop.py
import os, json, time, urllib.request
SOCK = os.environ["DEJIMA_SOCKET"]
ISLAND = os.environ["DEJIMA_PROJECT_NAME"]

def emit(event_type, payload=None):
    body = json.dumps({"island": ISLAND, "type": event_type,
                       "payload": payload or {}}).encode()
    req = urllib.request.Request(f"http://dejimad/v1/internal/agent-event",
                                 data=body, method="POST",
                                 headers={"Content-Type": "application/json"})
    # urllib over a unix socket needs a small shim — omitted here; or shell out to curl.
    ...

while True:
    task = next_task()      # your queue lookup
    if not task: time.sleep(5); continue
    result = run(task)      # your work
    emit("agent.task-complete", {"task_id": task.id})
```

Create the island with:

```bash
dejima init --repo https://github.com/me/my-agent \
  --agent headless --cmd "python my_loop.py"
dejima logs my-agent --follow
```

## Worked example: an Aider adapter (first-class type)

`image/agents/aider/init.sh`:

```sh
#!/usr/bin/env bash
set -euo pipefail

HOST_CFG="/opt/host/aider"
HOME_CFG="$HOME/.aider"
mkdir -p "$HOME_CFG"

# Mirror Anthropic / OpenAI keys from the host's mounted config.
if [[ -d "$HOST_CFG" ]]; then
    [[ -f "$HOST_CFG/.aider.env"  && ! -f "$HOME_CFG/.aider.env"  ]] && cp "$HOST_CFG/.aider.env"  "$HOME_CFG/.aider.env"
    [[ -f "$HOST_CFG/.aider.conf" && ! -f "$HOME_CFG/.aider.conf" ]] && cp "$HOST_CFG/.aider.conf" "$HOME_CFG/.aider.conf"
fi
```

Add to `image/start.sh`:

```sh
aider) AGENT_CMD="aider --no-auto-commits" ;;
```

Add to `image/Dockerfile`, after `USER dejima`:

```dockerfile
RUN pip install --user aider-chat
ENV PATH=/home/dejima/.local/bin:$PATH
```

Then: `dejima init --repo … --agent aider`.

---

## What you don't get automatically

- **Restart-on-crash.** Headless agents that exit take the container with
  them. Re-launch with `dejima wake`. A supervisor mode is on the roadmap
  and will land when there's demand for it.
- **Per-agent credential scopes.** All host credentials mounted into the
  image are visible to every agent that runs in any island. Granular
  scoping (per-island `gh` token, per-island OpenAI key) is a future
  surface.
- **A task model.** Today, an island has one current agent process. There's
  no concept of queued tasks, retries, parents/children, etc. By design — see
  the v1 spec for why.
