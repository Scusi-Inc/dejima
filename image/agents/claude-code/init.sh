#!/usr/bin/env bash
# Claude Code shim — runs inside the island when DEJIMA_AGENT=claude-code.
#
# Responsibilities:
#   1. Copy host Claude credentials into the agent's writable .claude dir.
#   2. Drop a CLAUDE.md template into the workspace if none exists.
#   3. Install hooks that emit agent.* events to dejimad over its Unix socket.

set -euo pipefail

HOST_CLAUDE="/opt/host/claude"
HOME_CLAUDE="$HOME/.claude"
mkdir -p "$HOME_CLAUDE/hooks"

# --- credentials -----------------------------------------------------------
if [[ -d "$HOST_CLAUDE" ]]; then
    for f in .credentials.json credentials.json settings.json; do
        if [[ -f "$HOST_CLAUDE/$f" && ! -f "$HOME_CLAUDE/$f" ]]; then
            cp "$HOST_CLAUDE/$f" "$HOME_CLAUDE/$f"
        fi
    done
fi

# --- CLAUDE.md template ----------------------------------------------------
TEMPLATE="/opt/dejima/agents/claude-code/CLAUDE.md"
TARGET="/workspace/CLAUDE.md"
if [[ -f "$TEMPLATE" && ! -f "$TARGET" ]]; then
    cp "$TEMPLATE" "$TARGET"
fi

# --- hooks ----------------------------------------------------------------
cp /opt/dejima/agents/claude-code/hooks/notify.sh "$HOME_CLAUDE/hooks/dejima-notify.sh"
chmod +x "$HOME_CLAUDE/hooks/dejima-notify.sh"

# Settings file wires the hook to Notification (Claude is waiting on user) and
# Stop (response finished). Only write if not already configured.
SETTINGS="$HOME_CLAUDE/settings.json"
if [[ ! -f "$SETTINGS" ]] || ! grep -q "dejima-notify" "$SETTINGS" 2>/dev/null; then
    cat > "$SETTINGS" <<EOF
{
  "hooks": {
    "Notification": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "\$HOME/.claude/hooks/dejima-notify.sh agent.waiting-for-input"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "\$HOME/.claude/hooks/dejima-notify.sh agent.task-complete"
          }
        ]
      }
    ]
  }
}
EOF
fi
