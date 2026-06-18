#!/usr/bin/env bash
# OpenClaw shim — runs inside the island when DEJIMA_AGENT=openclaw, before the
# gateway launches (start.sh runs this, then DEJIMA_LAUNCH starts the gateway).
#
# It bridges dejima's framework-agnostic LLM-credential contract into OpenClaw's
# native config (see docs/agent-adapters.md):
#   * DEJIMA_PROVIDER_KEY_FILE — path to a read-only 0600 .env (one provider) the
#     daemon materialized at /opt/host/llm/<provider>.env. We merge it into the
#     writable ~/.openclaw/.env (which OpenClaw reads for OPENAI_API_KEY etc.), so
#     the key is never a container env var and a rotation re-applies on restart.
#   * DEJIMA_MODEL — the "provider/model" string the user chose; we set it as the
#     gateway's default model.
# Both are optional: with neither set the gateway still boots (idle), and the
# missing-key state is surfaced by dejima's proactive agent health.

set -euo pipefail

OPENCLAW_HOME="$HOME/.openclaw"
mkdir -p "$OPENCLAW_HOME"

# --- provider key → ~/.openclaw/.env (idempotent, dejima-managed block) ----
KEY_FILE="${DEJIMA_PROVIDER_KEY_FILE:-}"
if [[ -n "$KEY_FILE" && -f "$KEY_FILE" ]]; then
    ENV_FILE="$OPENCLAW_HOME/.env"
    TMP="$ENV_FILE.dejima-tmp"
    # Drop any prior dejima-managed block (everything between the markers), then
    # re-append the current key file under fresh markers. Keeps user-authored
    # lines intact and makes key rotation a clean replace.
    if [[ -f "$ENV_FILE" ]]; then
        sed '/^# >>> dejima-managed >>>$/,/^# <<< dejima-managed <<<$/d' "$ENV_FILE" > "$TMP" || cp "$ENV_FILE" "$TMP"
    else
        : > "$TMP"
    fi
    {
        echo "# >>> dejima-managed >>>"
        cat "$KEY_FILE"
        echo "# <<< dejima-managed <<<"
    } >> "$TMP"
    mv "$TMP" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
fi

# --- model selection → gateway default -------------------------------------
if [[ -n "${DEJIMA_MODEL:-}" ]] && command -v openclaw >/dev/null 2>&1; then
    # Best-effort: the gateway also re-reads config on start, and a bad value
    # shouldn't abort the shim (start.sh runs under `set -e`).
    openclaw config set agents.defaults.model.primary "$DEJIMA_MODEL" || \
        echo "dejima: warning — could not set openclaw default model to '$DEJIMA_MODEL'" >&2
fi
