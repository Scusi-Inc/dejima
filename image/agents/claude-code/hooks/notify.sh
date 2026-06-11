#!/usr/bin/env bash
# Claude Code hook script: posts agent events to dejimad over its Unix socket.
#
# Installed at $HOME/.claude/hooks/notify.sh by image/agents/claude-code/init.sh.
# Wired up in $HOME/.claude/settings.json (also written by init.sh) for the
# Notification and Stop hook events.
#
# Trust model: anyone with access to /run/dejima/dejimad.sock can spoof
# agent-event payloads. In v1 the socket lives inside one container, which is
# the same trust boundary as the agent itself, so this is acceptable.

set -euo pipefail

SOCKET="${DEJIMA_SOCKET:-/run/dejima/dejimad.sock}"
ISLAND="${DEJIMA_PROJECT_NAME:-unknown}"
AGENT="${DEJIMA_AGENT_ID:-}"
TYPE="${1:-agent.waiting-for-input}"

if [[ ! -S "$SOCKET" ]]; then
    # Daemon socket not mounted; nothing to do.
    exit 0
fi

payload=$(cat 2>/dev/null || echo '{}')
body=$(jq -n \
    --arg island "$ISLAND" \
    --arg agent "$AGENT" \
    --arg type "$TYPE" \
    --argjson payload "$payload" \
    '{island: $island, agent: $agent, type: $type, payload: $payload}')

curl --silent --show-error --max-time 3 \
     --unix-socket "$SOCKET" \
     -H "Content-Type: application/json" \
     -X POST "http://dejimad/v1/internal/agent-event" \
     -d "$body" || true
