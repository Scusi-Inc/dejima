#!/usr/bin/env bash
# Dejima island entrypoint.
#
# Sets up git + gh credentials from host mounts, layers in the agent-specific
# shim (if any), clones the target repo (if not already present), starts a
# tmux session running the agent, and tails forever to keep the container up.

set -euo pipefail

PROJECT="${DEJIMA_PROJECT_NAME:-island}"
REPO="${DEJIMA_REPO_URL:-}"
AGENT="${DEJIMA_AGENT:-claude-code}"
WORKSPACE="/workspace"
SESSION="dejima"

mkdir -p "$HOME/.claude" "$HOME/.config"

# --- git identity from host (if mounted) ----------------------------------
if [[ -f /opt/host/gitconfig ]]; then
    HOST_NAME=$(GIT_CONFIG_GLOBAL=/opt/host/gitconfig git config --get user.name 2>/dev/null || true)
    HOST_EMAIL=$(GIT_CONFIG_GLOBAL=/opt/host/gitconfig git config --get user.email 2>/dev/null || true)
    if [[ -n "$HOST_NAME" ]];  then git config --global user.name  "$HOST_NAME";  fi
    if [[ -n "$HOST_EMAIL" ]]; then git config --global user.email "$HOST_EMAIL"; fi
fi

# --- gh credentials --------------------------------------------------------
# GH_CONFIG_DIR points at /opt/host/gh-config (read-only) if the host has gh.
# `gh auth setup-git` writes the credential helper into the agent's gitconfig.
if [[ -d "/opt/host/gh-config" ]]; then
    gh auth setup-git 2>/dev/null || echo "warning: gh auth setup-git failed; pushes may need a separate credential path"
fi

# --- per-agent shim --------------------------------------------------------
SHIM_DIR="/opt/dejima/agents/${AGENT}"
if [[ -x "${SHIM_DIR}/init.sh" ]]; then
    echo "running shim for agent ${AGENT}"
    "${SHIM_DIR}/init.sh"
fi

# --- clone the repo (idempotent) ------------------------------------------
if [[ -n "$REPO" && ! -d "${WORKSPACE}/.git" ]]; then
    echo "cloning ${REPO} into ${WORKSPACE}"
    # WORKSPACE is a volume mount; clone into a temp dir then move contents.
    TMP=$(mktemp -d)
    git clone "$REPO" "$TMP/repo"
    shopt -s dotglob nullglob
    mv "$TMP/repo/"* "${WORKSPACE}/"
    rm -rf "$TMP"
fi

cd "$WORKSPACE"

# --- agent command --------------------------------------------------------
case "$AGENT" in
    claude-code) AGENT_CMD="claude" ;;
    *)           AGENT_CMD="${AGENT}" ;;
esac

# --- start (or attach to existing) tmux session ---------------------------
if ! tmux has-session -t "$SESSION" 2>/dev/null; then
    tmux new-session -d -s "$SESSION" -c "$WORKSPACE" "$AGENT_CMD"
fi

echo "dejima island '${PROJECT}' ready; tmux session '${SESSION}' running ${AGENT_CMD}"
echo "attach with: docker exec -it dejima-${PROJECT} tmux attach-session -t ${SESSION}"

# Keep container alive while tmux server runs.
exec tail -f /dev/null
