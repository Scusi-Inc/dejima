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

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
info()  { printf '  %s\n' "$*"; }
fail()  { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

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
    git -C "$SRC_DIR" fetch --quiet --tags origin "$REF" || fail "couldn't fetch updates in $SRC_DIR — check your network, or remove it (\`rm -rf $SRC_DIR\`) and re-run."
    git -C "$SRC_DIR" checkout --quiet "$REF" || fail "couldn't check out '$REF' in $SRC_DIR — remove it (\`rm -rf $SRC_DIR\`) and re-run."
    git -C "$SRC_DIR" pull --quiet --ff-only origin "$REF" || true
else
    if [[ -e "$SRC_DIR" ]]; then
        info "Found a partial/incomplete checkout at $SRC_DIR (likely an interrupted run) — re-cloning"
        rm -rf "$SRC_DIR"
    fi
    info "Cloning Dejima to $SRC_DIR"
    git clone --quiet --branch "$REF" "$REPO_URL" "$SRC_DIR" \
        || fail "git clone failed — check your network and that '$REF' exists, then re-run."
fi

# --- Hand off to make setup ----------------------------------------------
command -v make >/dev/null 2>&1 || fail "make is required to build Dejima. On macOS run \`xcode-select --install\`; on Linux install build tools (e.g. \`sudo apt install make\`), then re-run."
echo
exec make -C "$SRC_DIR" setup
