#!/usr/bin/env bash
# Claude Code hook: posts agent events to dejimad over the token-authenticated,
# host-internal TCP path (DEJIMA_HOST + per-island DEJIMA_TOKEN).
#
# Installed at $HOME/.claude/hooks/notify.sh by image/agents/claude-code/init.sh,
# wired into $HOME/.claude/settings.json for the Notification and Stop hooks.
#
# The token is per-island and island-scoped (internal/api/tokenauth.go): the
# daemon attributes the event to the token's island, so a hook can only emit for
# its own island — it can't spoof another's. Best-effort: no-ops when the
# in-island autonomy path isn't configured.

set -euo pipefail

HOST="${DEJIMA_HOST:-}"
TOKEN="${DEJIMA_TOKEN:-}"
ISLAND="${DEJIMA_PROJECT_NAME:-unknown}"
AGENT="${DEJIMA_AGENT_ID:-}"
TYPE="${1:-agent.waiting-for-input}"

if [[ -z "$HOST" || -z "$TOKEN" ]]; then
    # In-island → daemon path not configured; nothing to do.
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
     -H "Authorization: Bearer ${TOKEN}" \
     -H "Content-Type: application/json" \
     -X POST "http://${HOST}/v1/internal/agent-event" \
     -d "$body" || true
