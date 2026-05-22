#!/usr/bin/env bash
# Codex CLI shim — runs inside the island when DEJIMA_AGENT=codex.
#
# Responsibilities:
#   1. Copy host Codex credentials into the agent's writable .codex dir.
#   2. Drop an AGENTS.md template into the workspace if none exists.

set -euo pipefail

HOST_CODEX="/opt/host/codex"
HOME_CODEX="$HOME/.codex"
mkdir -p "$HOME_CODEX"

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
