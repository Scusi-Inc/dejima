#!/usr/bin/env bash
# Codex CLI shim — runs inside the island when DEJIMA_AGENT=codex.
#
# Responsibilities:
#   1. Copy host Codex credentials into the agent's writable .codex dir.
#   2. Drop an AGENTS.md template into the workspace if none exists.
#   3. Install a notify hook so Codex turn-complete events flow into dejimad
#      (mirrors the Claude Code hook → agent.* events → TUI / webhooks).

set -euo pipefail

HOST_CODEX="/opt/host/codex"
HOME_CODEX="$HOME/.codex"
mkdir -p "$HOME_CODEX" "$HOME_CODEX/hooks"

# --- the binary actually being there ---------------------------------------
#
# codex ships its executable as an OPTIONAL per-platform npm dependency
# (@openai/codex-linux-arm64 and friends), and `npm install -g` SUCCEEDS WHEN AN
# OPTIONAL DEPENDENCY FAILS — that is npm's designed behaviour. So an image can
# build green with a codex that dies on first use:
#
#   Error: Missing optional dependency @openai/codex-linux-arm64.
#   Reinstall Codex: npm install -g @openai/codex@latest
#
# An operator hit exactly that: island up, agent "installed", binary absent, and
# nothing they could do at agent-creation time would have fixed it.
#
# The Dockerfile now verifies both CLIs at build time, but that only protects
# IMAGES BUILT AFTER IT. This repairs the ones already out there, at the moment
# the agent starts, which is the last point before a person meets the failure.
#
# Not fatal if it cannot be fixed: a codex that starts and complains is more
# useful than an island that refuses to come up, and the operator gets the exact
# command rather than an npm traceback.
if ! codex --version >/dev/null 2>&1; then
    echo "dejima: codex's platform binary is missing — reinstalling …" >&2
    if npm install -g @openai/codex@latest >/dev/null 2>&1 && codex --version >/dev/null 2>&1; then
        echo "dejima: codex reinstalled ($(codex --version 2>/dev/null))" >&2
    else
        echo "dejima: could not repair codex automatically." >&2
        echo "dejima: run this in the island, then restart the agent:" >&2
        echo "dejima:     npm install -g @openai/codex@latest" >&2
    fi
fi

# --- credentials -----------------------------------------------------------
if [[ -d "$HOST_CODEX" ]]; then
    for f in auth.json credentials.json config.toml; do
        if [[ -f "$HOST_CODEX/$f" && ! -f "$HOME_CODEX/$f" ]]; then
            cp "$HOST_CODEX/$f" "$HOME_CODEX/$f"
        fi
    done
fi

# --- AGENTS.md template ----------------------------------------------------
TEMPLATE="/opt/dejima/agents/codex/AGENTS.md"
TARGET="/workspace/AGENTS.md"
if [[ -f "$TEMPLATE" && ! -f "$TARGET" ]]; then
    cp "$TEMPLATE" "$TARGET"
fi

# --- notify hook ----------------------------------------------------------
# Codex spawns the configured `notify` command with one JSON-blob argument
# per event (see Codex docs, "notifications"). dejima-notify.sh decodes it and
# POSTs an agent.* event onto the dejimad Unix socket.
cp /opt/dejima/agents/codex/hooks/notify.sh "$HOME_CODEX/hooks/dejima-notify.sh"
chmod +x "$HOME_CODEX/hooks/dejima-notify.sh"

# Append `notify` to ~/.codex/config.toml only when it's missing — preserves
# any other settings copied from the host.
CONFIG="$HOME_CODEX/config.toml"
NOTIFY_LINE='notify = ["/home/dejima/.codex/hooks/dejima-notify.sh"]'
if [[ ! -f "$CONFIG" ]]; then
    echo "$NOTIFY_LINE" > "$CONFIG"
elif ! grep -qE '^[[:space:]]*notify[[:space:]]*=' "$CONFIG"; then
    printf '\n%s\n' "$NOTIFY_LINE" >> "$CONFIG"
fi

# --- island primer ---------------------------------------------------------
# Install the "you're in a Dejima island" primer into Codex's GLOBAL AGENTS.md
# (~/.codex/AGENTS.md) — additive to any repo AGENTS.md, idempotent,
# non-clobbering. Best-effort: a primer failure must never crash the container.
if [[ -x /opt/dejima/write-primer.sh ]]; then
    /opt/dejima/write-primer.sh "$HOME_CODEX/AGENTS.md" || true
fi
