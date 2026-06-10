#!/usr/bin/env bash
# Headless agent shim — runs inside the island when DEJIMA_AGENT=headless.
#
# Headless islands launch a user-supplied command (DEJIMA_AGENT_CMD) directly,
# without tmux. There's nothing agent-specific to set up here (no credential
# layout, no template files), but the shim exists so the contract is uniform
# and so future headless conveniences (e.g. tailing $HOME/.agent-state into a
# state file, or pre-installing a helper for /v1/internal/agent-event) have a
# natural place to live.
#
# Validate the command was provided up-front so the failure mode is clear
# (start.sh also checks; defense in depth).

set -euo pipefail

if [[ -z "${DEJIMA_AGENT_CMD:-}" ]]; then
    echo "dejima: agent=headless requires DEJIMA_AGENT_CMD to be set" >&2
    exit 64
fi

# A predictable place for the agent to drop status files, side artifacts, etc.
mkdir -p "$HOME/.agent-state"
