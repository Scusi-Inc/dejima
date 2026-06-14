#!/usr/bin/env bash
# Codex CLI hook: posts agent events to dejimad over the token-authenticated,
# host-internal TCP path (DEJIMA_HOST + per-island DEJIMA_TOKEN).
#
# Installed at $HOME/.codex/hooks/dejima-notify.sh by image/agents/codex/init.sh
# and wired into $HOME/.codex/config.toml via the top-level `notify` key.
#
# Codex invokes the notify command with one argument: a JSON blob describing the
# event. The canonical event is `agent-turn-complete`; anything else is forwarded
# as `agent.codex.<type>` so the daemon (and webhook subscribers) can see it.
#
# The token is per-island and island-scoped (internal/api/tokenauth.go): the
# daemon attributes the event to the token's island, so this hook can only emit
# for its own island. Best-effort: no-ops when the autonomy path isn't set.

set -euo pipefail

HOST="${DEJIMA_HOST:-}"
TOKEN="${DEJIMA_TOKEN:-}"
ISLAND="${DEJIMA_PROJECT_NAME:-unknown}"
AGENT="${DEJIMA_AGENT_ID:-}"
payload_json="${1:-{}}"

if [[ -z "$HOST" || -z "$TOKEN" ]]; then
    exit 0
fi

codex_type=$(printf '%s' "$payload_json" | jq -r '.type // empty' 2>/dev/null || true)

case "$codex_type" in
    agent-turn-complete) event_type="agent.task-complete" ;;
    "")                  event_type="agent.codex.unknown" ;;
    *)                   event_type="agent.codex.${codex_type}" ;;
esac

body=$(jq -n \
    --arg island "$ISLAND" \
    --arg agent  "$AGENT" \
    --arg type   "$event_type" \
    --argjson payload "$payload_json" \
    '{island: $island, agent: $agent, type: $type, payload: $payload}')

curl --silent --show-error --max-time 3 \
     -H "Authorization: Bearer ${TOKEN}" \
     -H "Content-Type: application/json" \
     -X POST "http://${HOST}/v1/internal/agent-event" \
     -d "$body" || true
