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
# Prefer the daemon-materialized seed: on macOS hosts the OAuth blob lives in
# the login Keychain, so /opt/host/claude never carries .credentials.json
# there. The seed also holds credentials sent via `dejima auth push`.
SEED_CLAUDE="/opt/host/claude-seed"
if [[ -f "$SEED_CLAUDE/.credentials.json" && ! -f "$HOME_CLAUDE/.credentials.json" ]]; then
    cp "$SEED_CLAUDE/.credentials.json" "$HOME_CLAUDE/.credentials.json"
    chmod 600 "$HOME_CLAUDE/.credentials.json"
fi

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
# The hook script and its settings.json wiring are daemon-OWNED managed files:
# re-derived from /opt on EVERY boot so a `dejima upgrade` (recreate against a
# fresh image) propagates fixes. Only user data is sticky. This is what heals the
# socket→TCP class of break: an island built from a stale image carried the OLD
# unix-socket hook + wiring, which silently no-op'd on a TCP-only island, so the
# agent-state heartbeat never fired (no mail-nudges, no idle-hibernate, no metric).
cp /opt/dejima/agents/claude-code/hooks/notify.sh "$HOME_CLAUDE/hooks/dejima-notify.sh"
chmod +x "$HOME_CLAUDE/hooks/dejima-notify.sh"

# Reconcile the Notification (Claude is waiting on user) and Stop (response
# finished) → dejima-notify wiring IDEMPOTENTLY every boot, rather than only when
# absent. We MERGE into the existing settings: any of the user's own hooks in
# these two events are preserved; only the dejima-owned entries are dropped and
# re-added to the current contract. All other settings keys are untouched.
SETTINGS="$HOME_CLAUDE/settings.json"
reconcile_dejima_hooks() {
    local cur='{}'
    if [[ -f "$SETTINGS" ]] && jq -e . "$SETTINGS" >/dev/null 2>&1; then
        cur=$(cat "$SETTINGS")
    fi
    # For each event, strip prior dejima-notify entries (refresh the contract),
    # keep the user's other hooks, then append our canonical entry.
    printf '%s' "$cur" | jq \
        --arg notif '$HOME/.claude/hooks/dejima-notify.sh agent.waiting-for-input' \
        --arg stop '$HOME/.claude/hooks/dejima-notify.sh agent.task-complete' '
        def reconcile(event; command):
            .hooks[event] = (
                ((.hooks[event] // [])
                 | map(select(any(.hooks[]?; .command | test("dejima-notify")) | not)))
                + [{ hooks: [{ type: "command", command: command }] }]
            );
        (. // {})
        | .hooks = (.hooks // {})
        | reconcile("Notification"; $notif)
        | reconcile("Stop"; $stop)
    '
}

if reconciled=$(reconcile_dejima_hooks 2>/dev/null) && [[ -n "$reconciled" ]]; then
    printf '%s\n' "$reconciled" >"$SETTINGS"
else
    # jq unavailable or a malformed pre-existing file: fall back to writing the
    # minimal canonical wiring so the heartbeat still works (last-resort; may
    # overwrite a hand-edited settings.json, but a dead heartbeat is worse).
    cat >"$SETTINGS" <<'EOF'
{
  "hooks": {
    "Notification": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "$HOME/.claude/hooks/dejima-notify.sh agent.waiting-for-input"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "$HOME/.claude/hooks/dejima-notify.sh agent.task-complete"
          }
        ]
      }
    ]
  }
}
EOF
fi
