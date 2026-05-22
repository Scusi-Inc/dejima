# Running a Custom Agent

The default Dejima island image bundles **Claude Code** and **Codex CLI**. Anything else — Aider, Goose, OpenHands, your own scripted agent — runs via a custom image.

This document walks through the path. Effort: 15–30 minutes once you've decided what you want installed.

---

## How agents plug into Dejima

The core architecture is agent-agnostic. The contract is just:

1. An executable on `PATH` that takes over a terminal (TTY).
2. Optional: a credential location on the host, mounted read-only into the island.
3. Optional: a workspace context file (CLAUDE.md, AGENTS.md, whatever your agent reads).

Everything else — container lifecycle, persistence, multi-attach, the websocket session API — works the same regardless of what's inside.

## Step-by-step: bundling a new agent

### 1. Fork the Dockerfile

```dockerfile
FROM dejima/island:latest

# Install your agent. Whatever method works:
RUN pip install aider-chat                  # Aider
# or
RUN npm install -g @some-vendor/agent       # an npm-distributed CLI
# or
RUN curl -fsSL https://example.com/install | sh
```

Build it: `docker build -t dejima/island-aider:latest -f Dockerfile.aider .`

### 2. (Optional) Add a shim under `image/agents/<your-agent>/`

If your agent needs host credentials mounted into the island, add a shim. This mirrors the pattern for Claude Code and Codex.

```bash
image/agents/aider/init.sh   # called by start.sh when DEJIMA_AGENT=aider
```

A minimal `init.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
# Aider reads OPENAI_API_KEY from env or ~/.aider.conf.yml.
# Mount and copy whatever your agent needs.
if [[ -f /opt/host/aider/.aider.conf.yml ]]; then
    cp /opt/host/aider/.aider.conf.yml "$HOME/.aider.conf.yml"
fi
```

To wire the host mount, you currently need to extend `credentialBindMounts()` in `internal/api/server.go`. (Roadmap: arbitrary host-mount config via project TOML so this doesn't require a Go change.)

### 3. Map the agent command in `start.sh`

Edit `image/start.sh`:

```bash
case "$AGENT" in
    claude-code) AGENT_CMD="claude" ;;
    codex)       AGENT_CMD="codex --sandbox-policy=no-sandbox" ;;
    aider)       AGENT_CMD="aider" ;;
    *)           AGENT_CMD="${AGENT}" ;;
esac
```

If your agent has its own OS-level sandboxing (Codex does), disable it — Docker is already the sandbox.

### 4. Use your custom image

```bash
dejima init \
  --repo git@github.com:you/foo.git \
  --agent aider \
  --image dejima/island-aider:latest
```

That's it. `dejima ls`, `dejima connect`, `dejima hibernate`, all the lifecycle verbs work the same way.

## Realistic Tier 2 agents worth considering

| Agent | Install | Credentials | Notes |
|---|---|---|---|
| **Aider** | `pip install aider-chat` | `~/.aider.conf.yml`, env vars | Lightweight, multi-LLM, fast to bundle. |
| **Goose** (Block) | `pipx install goose-ai` | `~/.config/goose/` | Newer, OSS, extension-friendly. |
| **OpenHands** | Docker image already; needs daemon | varies | Heavier setup, autonomous-loop oriented. |
| **Your own scripted agent** | however you ship it | however you ship it | Just install on `PATH` inside the image. |

If you build a working shim for a Tier 2 agent and want it upstreamed into the default image, send a PR — the criteria are: stable install path, clear credential story, no exotic OS dependencies.

## What you lose with a custom agent

Compared to Claude Code's first-class support:

- **No agent-event hooks.** Claude Code has a hooks system Dejima ties into to emit `agent.waiting-for-input` / `agent.task-complete` events to webhooks. Other agents either lack hooks or have different schemes. You'd implement equivalents per-agent.
- **No agent-specific docs.** The image's `AGENTS.md` template is generic; tailor it for the agent you're using.

Everything else — container isolation, persistence, multi-device, webhooks for lifecycle/presence events, the API — works identically.
