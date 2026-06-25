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
# Where a failed clone leaves its classified reason, for `dejima logs` follow-up
# and (later) a daemon-surfaced island event. The daemon reads this path.
CLONE_STATUS_FILE="/home/dejima/.dejima/clone-status"

# report_clone_failure turns a raw `git clone` error into actionable guidance and
# records why, so a failed clone is a live container with a clear message instead
# of a crashed one spewing a bare "fatal: Authentication failed". $1 is git's
# combined output.
report_clone_failure() {
    local err="$1" reason hint
    case "$err" in
        *"Authentication failed"*|*"could not read Username"*|*"Permission denied"*|*"terminal prompts disabled"*|*"HTTP 403"*|*"403 Forbidden"*)
            reason="auth"
            hint="this island can't authenticate to the git remote. Check its GitHub identity (\`dejima auth status\`) and (re)push a token (\`dejima auth push --github\`), then recreate the island or re-clone." ;;
        *"not found"*|*"Could not resolve host"*|*"does not exist"*)
            reason="not-found"
            hint="the remote couldn't be reached or found — check the repo URL, and that the identity can see it (private repos need a token with access)." ;;
        *)
            reason="error"
            hint="git couldn't clone the repo; the full output is above (\`dejima logs\` shows it)." ;;
    esac
    mkdir -p "$(dirname "$CLONE_STATUS_FILE")"
    printf '%s\n' "$reason" >"$CLONE_STATUS_FILE" 2>/dev/null || true
    {
        echo ""
        echo "dejima: ✗ repo clone failed (${reason}) — ${hint}"
        echo "dejima: the island is up; attach to fix git by hand, or recreate after fixing auth."
    } >&2
}
# The daemon supplies the primary agent's tmux session name and launch command
# (sourced from the handler registry) so they aren't duplicated here. Fallbacks
# keep the image runnable on its own (e.g. `docker run` for debugging).
SESSION="${DEJIMA_TMUX:-agent-a1}"
LAUNCH="${DEJIMA_LAUNCH:-}"

# /workspace and the per-agent state dir (e.g. ~/.claude) are named volumes. A
# volume mounted over a path the image didn't pre-create lands owned by root,
# leaving the `dejima` user unable to write into it — the clone or the agent
# shim's mkdir/cp then fails and the container crash-loops. Reclaim ownership of
# the mount points (dejima has passwordless sudo) so this self-heals on restart,
# even for volumes created before the image pre-created these paths. Shallow
# chown on the mount root is enough to unblock writes; existing nested files
# (e.g. a checked-out tree) keep their ownership.
sudo chown dejima:dejima "$WORKSPACE" "$HOME" "$HOME"/.claude "$HOME"/.codex "$HOME"/.agent-state 2>/dev/null || true

mkdir -p "$HOME/.claude" "$HOME/.config" "$HOME/.dejima/agents" "$WORKSPACE/.agents"

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
        # `if ! var=$(...)` keeps the failure out of set -e's reach so we can
        # report it instead of crashing the container.
        if clone_err=$(git clone "$SEED" "$TMP/repo" 2>&1); then
            if [[ -n "$REPO" ]]; then
                git -C "$TMP/repo" remote set-url origin "$REPO"
            else
                git -C "$TMP/repo" remote remove origin 2>/dev/null || true
            fi
        else
            echo "$clone_err" >&2
            report_clone_failure "$clone_err"
        fi
    elif [[ -n "$REPO" ]]; then
        echo "cloning ${REPO} into ${WORKSPACE}"
        if clone_err=$(git clone "$REPO" "$TMP/repo" 2>&1); then
            echo "$clone_err"
        else
            echo "$clone_err" >&2
            report_clone_failure "$clone_err"
        fi
    elif [[ -n "$SEED" ]]; then
        # SEED was requested but has no .git — usually an empty or unshared
        # bind-mount (e.g. a macOS seed path Docker doesn't share). Don't fail
        # silently: /workspace won't be a git repo, so multi-agent worktrees
        # can't be created.
        echo "dejima: WARNING seed ${SEED} has no .git (empty/unshared bind-mount?) — ${WORKSPACE} will not be a git repo; multi-agent worktrees will fail" >&2
    fi
    if [[ -d "$TMP/repo" ]]; then
        # WORKSPACE is a volume mount; move cloned contents (incl. dotfiles) in.
        shopt -s dotglob nullglob
        mv "$TMP/repo/"* "${WORKSPACE}/"
    fi
    rm -rf "$TMP"
fi

cd "$WORKSPACE"

# --- keep the container alive; the daemon launches every agent ------------
# Path B (island PID-1 unification): the entrypoint NO LONGER launches any
# agent. It is always a keepalive — PID 1 = `tail -f /dev/null` — so the
# container outlives every agent and no agent is ever PID 1. The daemon brings
# up ALL agents (interactive and headless, first and co-located) uniformly via
# `docker exec tmux new-session` (reconcileAgents / ensureAgentSession), so
# adding, removing, and reordering any agent is symmetric, and zero-agent
# islands work for every type. See docs/island-pid1-unification.md.
#
# (SESSION/LAUNCH/DEJIMA_AGENT_CMD are no longer read here; the daemon owns
# launch. DEJIMA_AGENT still selects the boot shim above.)
echo "dejima island '${PROJECT}' ready; agents managed by the daemon (attach: dejima connect ${PROJECT})"
exec tail -f /dev/null
