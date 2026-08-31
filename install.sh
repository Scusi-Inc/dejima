#!/usr/bin/env bash
# Dejima one-line installer.
#
# Usage:
#   curl -fsSL https://dejima.tech/install.sh | bash
#   # (raw fallback while DNS propagates:)
#   curl -fsSL https://raw.githubusercontent.com/aoos/dejima/master/install.sh | bash
#
# What it does:
#   1. Verifies prerequisites (git, curl, make; on macOS, Homebrew) — each with a
#      named cause + fix if absent.
#   2. Installs Go if missing.
#   3. Clones Dejima to ~/.dejima-src (or pulls latest if already there).
#      Interrupt/retry-safe: a partial checkout from a Ctrl-C'd run is re-cloned.
#   4. Hands off to `make setup`, which:
#      - Detects Docker; offers to install Docker Desktop via brew if missing
#      - Builds the `dejima` + `dejimad` binaries
#      - Installs them to /usr/local/bin (asks for sudo)
#      - Builds the dejima/island Docker image
#      - Registers dejimad as a launchd (macOS) or systemd user unit (Linux)
#      - Runs `dejima doctor` to verify
#
# Environment knobs:
#   DEJIMA_SRC_DIR    where to clone the source (default ~/.dejima-src)
#   DEJIMA_REF        git ref to check out (default master)
#   NOTIFY_URL        ntfy.sh / webhook URL to subscribe at service install
#   PREFIX            install prefix for binaries (default /usr/local)
#   AUTO_INSTALL_DOCKER=1  skip the Docker-install confirmation prompt

set -euo pipefail

REPO_URL="https://github.com/aoos/dejima.git"
SRC_DIR="${DEJIMA_SRC_DIR:-$HOME/.dejima-src}"
REF="${DEJIMA_REF:-master}"
OS=$(uname -s)

# Record everything to a file before anything else runs, so a failure is
# reportable. See scripts/lib/transcript.sh — but that lives in the repo we have
# not cloned yet, so the one-liner carries its own copy of the two lines that
# matter.
if [[ -z "${DEJIMA_INSTALL_LOG:-}" ]]; then
    DEJIMA_INSTALL_LOG="${HOME:-/tmp}/dejima-install-$(date +%Y%m%d-%H%M%S).log"
    if : >"$DEJIMA_INSTALL_LOG" 2>/dev/null; then
        export DEJIMA_INSTALL_LOG
        {
            printf 'dejima install transcript\ndate:    %s\nhost:    %s\nref:     %s\n\n' \
                "$(date)" "$(uname -a 2>/dev/null || echo unknown)" "${DEJIMA_REF:-master}"
        } >>"$DEJIMA_INSTALL_LOG"
        # tee, so the operator still sees the install. This makes stdout a pipe —
        # the condition #341 wrongly read as "nobody is here". lib/tty.sh answers
        # that on /dev/tty, so prompts and sudo still find the human.
        exec > >(tee -a "$DEJIMA_INSTALL_LOG") 2>&1
    else
        unset DEJIMA_INSTALL_LOG
    fi
fi

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
info()  { printf '  %s\n' "$*"; }
fail()  {
    printf '\033[31m✗\033[0m %s\n' "$*" >&2
    if [[ -n "${DEJIMA_INSTALL_LOG:-}" ]]; then
        printf '\n  A full transcript of this run is at:\n    %s\n' "$DEJIMA_INSTALL_LOG" >&2
        printf '  Send that file — it says what actually happened, in order.\n' >&2
    fi
    exit 1
}
# A ref can be a branch, a tag, or a raw commit SHA. `git clone --branch` and a
# refspec fetch only accept a branch/tag name — a bare SHA makes them fail with
# "Remote branch <sha> not found". So when the ref looks like a commit hash we
# clone the whole repo (all branches + tags) and check the commit out directly.
# The launch gate pins a frozen SHA this way (e.g. DEJIMA_REF=99001ba…).
is_commit_sha() { [[ "$1" =~ ^[0-9a-f]{7,40}$ ]]; }

bold "Dejima installer"
info "source:  $SRC_DIR (ref: $REF)"
info "binaries will be installed to ${PREFIX:-/usr/local}/bin"
echo

# --- Prereqs --------------------------------------------------------------
command -v git >/dev/null 2>&1 || fail "git is required. On macOS run \`xcode-select --install\` (installs git); on Linux install it with your distro's package manager (e.g. \`sudo apt install git\`)."
command -v curl >/dev/null 2>&1 || fail "curl is required to fetch dependencies (install it via your package manager and re-run)."

case "$OS" in
    Darwin)
        if ! command -v brew >/dev/null 2>&1; then
            fail "Homebrew is required on macOS (Dejima installs Go/Docker through it). Install it from https://brew.sh and re-run."
        fi
        ;;
    Linux)
        ;;
    *)
        fail "unsupported OS: $OS (Dejima v1 supports macOS and Linux)"
        ;;
esac

if ! command -v go >/dev/null 2>&1; then
    if [[ "$OS" == "Darwin" ]]; then
        info "Installing Go via Homebrew (one-time)…"
        brew install go
    else
        fail "Go is required. Install from https://go.dev/dl/ (or via your distro's package manager) and re-run."
    fi
fi

# --- Fetch source ---------------------------------------------------------
# Idempotent + interrupt-safe: a re-run (or a retry after a Ctrl-C mid-clone)
# must never brick. We treat the checkout as recoverable, never fatal:
#   · a valid checkout      → fetch + checkout the ref (update in place)
#   · a partial/non-git dir → discard and re-clone (an interrupted clone leaves
#                             a non-empty, non-.git directory; a plain `git clone`
#                             into it would fail with "destination already exists")
# Tags are fetched (not a --depth 1 shallow clone): the build bakes its version
# from `git describe --tags`, so the tags must be present or the install reports
# a bare commit hash instead of a real release.
if [[ -d "$SRC_DIR/.git" ]] && git -C "$SRC_DIR" rev-parse --git-dir >/dev/null 2>&1; then
    info "Updating existing checkout at $SRC_DIR"
    if is_commit_sha "$REF"; then
        # A bare SHA can't be a fetch refspec — pull every branch so the commit
        # is reachable locally, then detach onto it.
        git -C "$SRC_DIR" fetch --quiet --tags origin || fail "couldn't fetch updates in $SRC_DIR — check your network, or remove it (\`rm -rf $SRC_DIR\`) and re-run."
        git -C "$SRC_DIR" checkout --quiet "$REF" || fail "couldn't check out commit '$REF' in $SRC_DIR — remove it (\`rm -rf $SRC_DIR\`) and re-run."
    else
        git -C "$SRC_DIR" fetch --quiet --tags origin "$REF" || fail "couldn't fetch updates in $SRC_DIR — check your network, or remove it (\`rm -rf $SRC_DIR\`) and re-run."
        git -C "$SRC_DIR" checkout --quiet "$REF" || fail "couldn't check out '$REF' in $SRC_DIR — remove it (\`rm -rf $SRC_DIR\`) and re-run."
        git -C "$SRC_DIR" pull --quiet --ff-only origin "$REF" || true
    fi
else
    if [[ -e "$SRC_DIR" ]]; then
        info "Found a partial/incomplete checkout at $SRC_DIR (likely an interrupted run) — re-cloning"
        rm -rf "$SRC_DIR"
    fi
    info "Cloning Dejima to $SRC_DIR"
    if is_commit_sha "$REF"; then
        # `--branch` rejects a SHA; clone the repo (all branches + tags), then
        # check the commit out directly.
        git clone --quiet "$REPO_URL" "$SRC_DIR" \
            || fail "git clone failed — check your network, then re-run."
        git -C "$SRC_DIR" checkout --quiet "$REF" \
            || fail "couldn't check out commit '$REF' — verify it exists on a pushed branch, then re-run."
    else
        git clone --quiet --branch "$REF" "$REPO_URL" "$SRC_DIR" \
            || fail "git clone failed — check your network and that '$REF' exists, then re-run."
    fi
fi

# --- Hand off to make setup ----------------------------------------------
command -v make >/dev/null 2>&1 || fail "make is required to build Dejima. On macOS run \`xcode-select --install\`; on Linux install build tools (e.g. \`sudo apt install make\`), then re-run."
echo
exec make -C "$SRC_DIR" setup
