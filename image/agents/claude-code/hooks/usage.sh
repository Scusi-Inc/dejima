#!/usr/bin/env bash
# Claude Code hook: reports CUMULATIVE token usage for the session to dejimad as
# an agent.usage event, over the token-authenticated in-island path (same path
# and anti-spoof model as notify.sh — the daemon attributes the event to the
# token's island). Wired to the Stop hook by image/agents/claude-code/init.sh.
#
# Dejima can't see the (opaque, outbound) LLM call, so the daemon gets token
# counts FROM here and derives cost itself. Best-effort: no-ops without the
# autonomy path, without jq, or if the transcript can't be read.
#
# The Stop hook receives {transcript_path, session_id, ...} on stdin. The
# transcript is JSONL where each assistant line carries message.usage for ONE
# billed API call (input/cache/output are per-call), so summing across assistant
# messages yields the session's cumulative usage — the same basis as billing.

set -euo pipefail

HOST="${DEJIMA_HOST:-}"
TOKEN="${DEJIMA_TOKEN:-}"
ISLAND="${DEJIMA_PROJECT_NAME:-unknown}"
AGENT="${DEJIMA_AGENT_ID:-}"

if [[ -z "$HOST" || -z "$TOKEN" ]]; then
    exit 0 # in-island → daemon path not configured; nothing to do.
fi
command -v jq >/dev/null 2>&1 || exit 0

input=$(cat 2>/dev/null || echo '{}')
transcript=$(printf '%s' "$input" | jq -r '.transcript_path // empty' 2>/dev/null || true)
if [[ -z "$transcript" || ! -f "$transcript" ]]; then
    exit 0
fi

# Stream the JSONL (no slurp — transcripts grow large) and reduce the per-call
# usage into session totals, keeping the most recent assistant model id.
totals=$(jq -rn '
  reduce (inputs | select(.type == "assistant") | .message) as $m
    ({i:0, cc:0, cr:0, o:0, model:""};
       .i     += (($m.usage.input_tokens) // 0)
     | .cc    += (($m.usage.cache_creation_input_tokens) // 0)
     | .cr    += (($m.usage.cache_read_input_tokens) // 0)
     | .o     += (($m.usage.output_tokens) // 0)
     | .model  = (($m.model) // .model))
  | {input_tokens: .i, cache_creation_input_tokens: .cc,
     cache_read_input_tokens: .cr, output_tokens: .o, model: .model}
' "$transcript" 2>/dev/null || true)

if [[ -z "$totals" ]]; then
    exit 0
fi

# Tag the reporting adapter so the daemon (and the TUI) can show provenance.
payload=$(printf '%s' "$totals" | jq -c '. + {source: "claude-code"}' 2>/dev/null || true)
[[ -z "$payload" ]] && exit 0

body=$(jq -n \
    --arg island "$ISLAND" \
    --arg agent "$AGENT" \
    --argjson payload "$payload" \
    '{island: $island, agent: $agent, type: "agent.usage", payload: $payload}')

curl --silent --show-error --max-time 3 \
     -H "Authorization: Bearer ${TOKEN}" \
     -H "Content-Type: application/json" \
     -X POST "http://${HOST}/v1/internal/agent-event" \
     -d "$body" || true
