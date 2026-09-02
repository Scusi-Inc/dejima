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
# argv[1] is the event JSON.
#
# NOT "${1:-{}}". In that form the first `}` closes the expansion, so the default
# is `{` and a literal `}` is appended to whatever comes out — meaning a supplied
# argument comes back as `{"type":"agent-turn-complete"}}` and every real
# invocation produces invalid JSON. jq then fails, `set -e` exits 2, and no event
# is ever posted.
#
# The only path that parsed was the no-argument default, which is the one Codex
# never takes: the hook worked exactly when it had nothing to report. shellcheck
# passes it clean, because it is valid shell that means something other than it
# looks like. Codex agents had therefore never emitted a single event.
payload_json="${1-}"
if [[ -z "$payload_json" ]]; then
    payload_json='{}'
fi

# A payload we cannot parse must not take the hook down with it. jq dying under
# `set -e` is exactly how this went quiet — no event, no error, nothing on screen
# — so degrade to an empty payload and still report that the event happened.
if ! printf '%s' "$payload_json" | jq -e . >/dev/null 2>&1; then
    payload_json='{}'
fi

# A hook that cannot post must leave a trace. Best-effort means it does not
# crash the agent; it does not mean it vanishes.
#
# This was `exit 0` with no output, which made three different situations
# indistinguishable: the hook never ran, the hook ran and the daemon rejected
# it, and the autonomy variables are not in this agent's environment. "Codex
# status never updates" then reads as a missing feature rather than a broken
# one — the same shape that cost an evening on WSL.
#
# Written once per container, not per event: the condition is static, and a line
# per turn would bury it.
LOG="${DEJIMA_NOTIFY_LOG:-${HOME:-/tmp}/.codex/dejima-notify.log}"

note() {
    mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
    printf '%s %s\n' "$(date -Is 2>/dev/null || echo -)" "$1" >> "$LOG" 2>/dev/null || true
}

note_once() {
    local marker="$LOG.$1"
    [[ -e "$marker" ]] && return 0
    mkdir -p "$(dirname "$marker")" 2>/dev/null || true
    : >> "$marker" 2>/dev/null || true
    note "$2"
}

if [[ -z "$HOST" || -z "$TOKEN" ]]; then
    note_once unconfigured \
        "not posting: DEJIMA_HOST and/or DEJIMA_TOKEN are unset in this agent's environment, so codex events cannot reach dejimad and this agent will show no status."
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

# Likewise: a rejected or unreachable POST says so. `|| true` on its own is the
# same silence one layer down.
if ! err=$(curl --silent --show-error --fail --max-time 3 \
     -H "Authorization: Bearer ${TOKEN}" \
     -H "Content-Type: application/json" \
     -X POST "http://${HOST}/v1/internal/agent-event" \
     -d "$body" 2>&1); then
    note "posting ${event_type} to ${HOST} failed: ${err}"
fi
