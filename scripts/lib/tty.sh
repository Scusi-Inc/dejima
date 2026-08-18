# shellcheck shell=bash
# Terminal, prompting, and sudo pre-authorization helpers for the installer.
#
# Split out of scripts/setup.sh so the decisions in here can be tested. They are
# the ones that broke a fresh Mac mini install in the field (#341), and they are
# untestable in place: setup.sh installs Docker and writes to /usr/local, so no
# test can run it, and the case that matters most — a person present at the
# keyboard while stdin is a pipe — cannot be produced by running a script the
# ordinary way. See scripts/lib/tty_test.sh, which drives all three shapes under
# a real pty.
#
# Sourced by setup.sh; expects `set -euo pipefail` from the caller.

# ---------------------------------------------------------------------------
# Terminal output
# ---------------------------------------------------------------------------
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }
info()   { printf '  %s\n' "$*"; }
ok()     { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn()   { printf '  \033[33m!\033[0m %s\n' "$*"; }
fail()   { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; }

# ---------------------------------------------------------------------------
# Finding the human
# ---------------------------------------------------------------------------
# `curl -fsSL … | bash` makes stdin the PIPE, so `[[ -t 0 ]]` is false even
# though someone is sitting right there watching the output scroll past. The
# installer used that test as its "is anyone home?" check, and on a fresh Mac
# mini it produced two failures at once (#341): every prompt silently
# self-answered yes — Docker and Tailscale installed without being asked for —
# and sudo pre-authorization was skipped, so Homebrew's own sudo surfaced
# mid-cask as a bare `Password:` that took the password with echo still on and
# then hung.
#
# /dev/tty is the controlling terminal no matter what stdin is wired to. Open it
# once here; every prompt and every sudo below reads from it. When it can't be
# opened there genuinely is no human (CI, a detached service) and we fall back to
# the non-interactive defaults.
#
# The open is attempted in a subshell first: a redirection failure on `exec` in
# the main shell is fatal under `set -e`, which would turn "no terminal" into a
# crash on exactly the headless hosts that branch exists to serve.
TTY_FD=""
if ( exec </dev/tty ) 2>/dev/null; then
    exec 3<>/dev/tty
    TTY_FD=3
fi

have_tty() { [[ -n "$TTY_FD" ]]; }

# Run a command with stdin attached to the terminal rather than to whatever
# stdin this script inherited. Homebrew changes how it asks for a password based
# on whether stdin is a tty — handed the exhausted curl pipe it reads EOF with
# echo left on, which is the "password in the clear" half of #341.
with_tty() {
    if have_tty; then "$@" </dev/tty; else "$@"; fi
}

# prompt_yn <question> [default]  → 0 for yes, 1 for no.
# With no terminal, or with AUTO_INSTALL_DOCKER=1, returns the default without
# asking. The prompt is written to /dev/tty rather than stdout so it is visible
# even when the caller's output is being piped or teed.
prompt_yn() {
    local prompt="$1"
    local default="${2:-y}"
    if [[ "${AUTO_INSTALL_DOCKER:-}" == "1" ]] || ! have_tty; then
        [[ "$default" == "y" ]]
        return $?
    fi
    local reply
    printf '%s [Y/n] ' "$prompt" >/dev/tty
    read -r -u "$TTY_FD" reply || reply=""
    reply=${reply:-$default}
    case "$reply" in
        [Yy]|[Yy][Ee][Ss]) return 0 ;;
        *) return 1 ;;
    esac
}

# ---------------------------------------------------------------------------
# sudo pre-authorization
# ---------------------------------------------------------------------------
# Homebrew cask installs ask for a password PARTWAY THROUGH, not up front: the
# Docker cask links its CLI into /usr/local, which on Apple Silicon is outside
# the /opt/homebrew prefix and root-owned. That prompt lands on a terminal
# Homebrew is already capturing for its own `==>` output, and the observed
# result on a fresh Mac mini is the password echoed in the clear followed by an
# apparent hang. sudo's timestamp is per-tty and brew inherits ours, so priming
# it here means brew's own sudo calls find a warm ticket and never prompt.
SUDO_KEEPALIVE_PID=""

prime_sudo() {
    local reason="$1"
    if [[ "$(id -u)" == "0" ]]; then return 0; fi
    if ! command -v sudo >/dev/null 2>&1; then return 0; fi
    # No terminal means no place to prompt. Let the child fail on its own terms
    # rather than blocking a scripted run on a password nobody can type. Note
    # this asks whether a TERMINAL exists, not whether stdin is one — under
    # `curl | bash` stdin is the pipe but the user is present and can type.
    if ! have_tty; then return 0; fi

    if ! sudo -n -v 2>/dev/null; then
        info ""
        info "$reason needs your password partway through (Homebrew links binaries"
        info "into root-owned /usr/local). Asking now, once, so the installer doesn't"
        info "stop to ask in the middle of its own output:"
        # shellcheck disable=SC2024
        # The redirect running as the caller is the point: /dev/tty is ours, and
        # handing sudo the terminal instead of an exhausted pipe is the fix.
        if ! sudo -v </dev/tty; then
            warn "couldn't pre-authorize sudo — the installer may prompt mid-run"
            return 0
        fi
    fi

    # A cask download can outlast sudo's 5-minute timestamp, so hold it open.
    # The loop exits on its own if this script dies without calling the stop.
    # Only ever one: this is called at several steps, and overwriting the pid
    # would strand the previous loop until the script exits.
    if [[ -n "$SUDO_KEEPALIVE_PID" ]] && kill -0 "$SUDO_KEEPALIVE_PID" 2>/dev/null; then
        return 0
    fi
    ( while kill -0 "$$" 2>/dev/null; do
          sudo -n -v 2>/dev/null || true
          sleep 60
      done ) &
    SUDO_KEEPALIVE_PID=$!
}

stop_sudo_keepalive() {
    if [[ -n "$SUDO_KEEPALIVE_PID" ]]; then
        kill "$SUDO_KEEPALIVE_PID" 2>/dev/null || true
        wait "$SUDO_KEEPALIVE_PID" 2>/dev/null || true
        SUDO_KEEPALIVE_PID=""
    fi
}
