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
