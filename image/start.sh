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

# /workspace and the per-agent state dir (e.g. ~/.claude) are named volumes. A
# volume mounted over a path the image didn't pre-create lands owned by root,
# leaving the `dejima` user unable to write into it — the clone or the agent
# shim's mkdir/cp then fails and the container crash-loops. Reclaim ownership of
# the mount points (dejima has passwordless sudo) so this self-heals on restart,
# even for volumes created before the image pre-created these paths. Shallow
# chown on the mount root is enough to unblock writes; existing nested files
# (e.g. a checked-out tree) keep their ownership.
sudo chown dejima:dejima "$WORKSPACE" "$HOME"/.claude "$HOME"/.codex "$HOME"/.agent-state 2>/dev/null || true

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
# That helper only covers HTTPS remotes, and the island has no SSH keys — so
# rewrite SSH GitHub URLs to HTTPS, or a repo seeded with an SSH origin could
# never push.
if [[ -d "/opt/host/gh-config" ]]; then
    gh auth setup-git 2>/dev/null || echo "warning: gh auth setup-git failed; pushes may need a separate credential path"
    git config --global url."https://github.com/".insteadOf "git@github.com:"
fi

# --- per-agent shim --------------------------------------------------------
SHIM_DIR="/opt/dejima/agents/${AGENT}"
if [[ -x "${SHIM_DIR}/init.sh" ]]; then
    echo "running shim for agent ${AGENT}"
    "${SHIM_DIR}/init.sh"
fi

# --- clone the repo (idempotent) ------------------------------------------
# Two sources, in priority order:
#   * DEJIMA_SEED — a read-only host repo mounted at /opt/host/seed. We clone
#     from it (capturing local/unpushed commits) into the island's own volume,
#     then repoint origin at the real upstream (REPO) so `git push` works. The
#     workspace ends up fully independent of the host copy.
#   * REPO — a remote URL cloned directly (origin is correct out of the box).
SEED="${DEJIMA_SEED:-}"
if [[ ! -d "${WORKSPACE}/.git" ]]; then
    TMP=$(mktemp -d)
    if [[ -n "$SEED" && -d "${SEED}/.git" ]]; then
        echo "seeding ${WORKSPACE} from local copy ${SEED}"
        git clone "$SEED" "$TMP/repo"
        if [[ -n "$REPO" ]]; then
            git -C "$TMP/repo" remote set-url origin "$REPO"
        else
            git -C "$TMP/repo" remote remove origin 2>/dev/null || true
        fi
    elif [[ -n "$REPO" ]]; then
        echo "cloning ${REPO} into ${WORKSPACE}"
        git clone "$REPO" "$TMP/repo"
    fi
    if [[ -d "$TMP/repo" ]]; then
        # WORKSPACE is a volume mount; move cloned contents (incl. dotfiles) in.
        shopt -s dotglob nullglob
        mv "$TMP/repo/"* "${WORKSPACE}/"
    fi
    rm -rf "$TMP"
fi

cd "$WORKSPACE"

# --- agent command --------------------------------------------------------
# Each known agent maps to its binary + any flags needed when running inside a
# container. Codex's own OS-level sandboxing (Seatbelt/Landlock) doesn't apply
# inside Docker, so we disable it and rely on the container as the sandbox.
case "$AGENT" in
    claude-code) AGENT_CMD="claude" ;;
    codex)       AGENT_CMD="codex --sandbox-policy=no-sandbox" ;;
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
