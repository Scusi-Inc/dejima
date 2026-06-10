#!/usr/bin/env bash
# Codex CLI hook script: posts agent events to dejimad over its Unix socket.
#
# Installed at $HOME/.codex/hooks/dejima-notify.sh by image/agents/codex/init.sh
# and wired into $HOME/.codex/config.toml via the top-level `notify` key.
#
# Codex invokes the configured notify command with a single argument: a JSON
# blob describing the event (see Codex docs, "notifications"). The canonical
# event we care about today is `agent-turn-complete`; anything else gets
# forwarded with a `agent.codex.<type>` event name so the daemon (and any
# webhook subscribers) can see it without us having to teach this script every
# possible Codex type.
#
# Trust model is the same as the Claude hook: anyone with access to the
# socket inside the container can spoof; that boundary is the agent itself.

set -euo pipefail

SOCKET="${DEJIMA_SOCKET:-/run/dejima/dejimad.sock}"
ISLAND="${DEJIMA_PROJECT_NAME:-unknown}"
payload_json="${1:-{}}"

if [[ ! -S "$SOCKET" ]]; then
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
    --arg type   "$event_type" \
    --argjson payload "$payload_json" \
    '{island: $island, type: $type, payload: $payload}')

curl --silent --show-error --max-time 3 \
     --unix-socket "$SOCKET" \
     -H "Content-Type: application/json" \
     -X POST "http://dejimad/v1/internal/agent-event" \
     -d "$body" || true
