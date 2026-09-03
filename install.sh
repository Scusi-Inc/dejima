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
        # A FIFO plus an explicit wait, NOT `exec > >(tee …)`: process
        # substitution gives no PID to wait for, so on a fast exit the shell is
        # gone before tee drains and the log keeps only the header — precisely
        # the run whose log matters most.
        DEJIMA_FIFO="$(mktemp -u)"
        if mkfifo "$DEJIMA_FIFO" 2>/dev/null; then
            tee -a "$DEJIMA_INSTALL_LOG" <"$DEJIMA_FIFO" &
            DEJIMA_LOG_TEE=$!
            exec >"$DEJIMA_FIFO" 2>&1
            rm -f "$DEJIMA_FIFO"
            trap 'exec 1>&- 2>&-; wait "$DEJIMA_LOG_TEE" 2>/dev/null' EXIT
        else
            unset DEJIMA_INSTALL_LOG
        fi
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

# --- Which install is this? ----------------------------------------------
# ASKED FIRST, before Go, Docker, or a clone. This script builds the SERVER
# stack; install-client.sh drops a ~15MB CLI and nothing else.
#
# It used to be one road. Someone who only wanted to drive a server elsewhere
# was walked through a Go toolchain, a Homebrew Docker Desktop install, an image
# build and a launchd service — with no signpost that a one-minute script
# existed. Every failure they could hit was in work they never needed to do, and
# a fresh-Mac install failed three times in the field that way.
#
# /dev/tty, not stdin: `curl … | bash` makes stdin a pipe while a person watches
# from the keyboard, which is exactly the misreading that produced #341. Piped
# and non-interactive runs fall through to the server path, which is what an
# unattended installer should do.
# `[[ -e /dev/tty ]]` is the WRONG test and this is the third variant of that
# mistake this week: the device node exists on a process with no controlling
# terminal, and opening it then fails with ENXIO. Probe by OPENING it, in a
# subshell so a failed redirect is not fatal under `set -e` — which is exactly
# what scripts/lib/tty.sh does, for exactly this reason. That library is not
# available here; the repo has not been cloned yet.
if [[ -z "${DEJIMA_ROLE:-}" ]] && ( exec </dev/tty ) 2>/dev/null; then
    echo
    info "Two ways to run Dejima:"
    info "  [1] SERVER  — this machine hosts the islands (needs Docker; ~10 min)"
    info "  [2] CLIENT  — this machine drives a Dejima server somewhere else (~1 min)"
    echo
    printf '  Which is this? [1/2] (default 1): '
    read -r role </dev/tty 2>/dev/null || role=""
    case "$role" in
        2)
            echo
            bold "That's the client install — this script builds the server."
            info "Run this instead (no Go, no Docker, no daemon):"
            echo
            info "    curl -fsSL https://dejima.tech/install-client.sh | bash"
            echo
            info "On Windows, use PowerShell:"
            info "    irm https://dejima.tech/install-client.ps1 | iex"
            echo
            exit 0
            ;;
    esac
fi

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

# Go, which this installer needs because it BUILDS Dejima from source.
#
# macOS has always installed it via Homebrew. Linux said "Go is required, go get
# it from go.dev" and stopped — so the documented "install on a Mac mini or Linux
# box" path worked on one of those and dead-ended on the other, at the very first
# step, for anyone without a toolchain. That is most people; a server is not a
# development machine.
#
# Installed the same way the Go project recommends: the official tarball into
# /usr/local/go. Deliberately NOT the distro package — Debian and Ubuntu ship a Go
# well behind go.mod's requirement, so `apt install golang-go` succeeds and then
# the build fails with a version error, which is a worse failure than this one
# because it arrives later and names the wrong thing.
install_go_linux() {
    local want arch file url sums sha tmp
    # From go.mod, so this cannot drift from what the build actually needs.
    want="$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$SRC_DIR/go.mod" 2>/dev/null | head -1)"
    [[ -n "$want" ]] || want="1.26.3"
    case "$(uname -m)" in
        x86_64)        arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) fail "no prebuilt Go for $(uname -m) — install Go $want from https://go.dev/dl/ and re-run." ;;
    esac
    file="go${want}.linux-${arch}.tar.gz"
    url="https://go.dev/dl/${file}"

    info "Installing Go ${want} (one-time, ~70MB) …"
    tmp="$(mktemp -d)"
    curl -fsSL "$url" -o "$tmp/$file" \
        || fail "couldn't download $url — check the network and re-run."

    # VERIFY BEFORE EXTRACTING. This tarball becomes the compiler that builds a
    # daemon which runs as a service and holds credentials; taking it on trust
    # because it came over HTTPS is the wrong standard for that. go.dev publishes
    # the checksums, so there is no excuse not to check.
    sums="$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' 2>/dev/null || true)"
    sha="$(printf '%s' "$sums" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for rel in data:
    for f in rel.get('files', []):
        if f.get('filename') == '$file':
            print(f.get('sha256', ''))
            sys.exit(0)
" 2>/dev/null || true)"
    if [[ -n "$sha" ]]; then
        local got
        got="$(sha256sum "$tmp/$file" 2>/dev/null | awk '{print $1}')"
        [[ -z "$got" ]] && got="$(shasum -a 256 "$tmp/$file" 2>/dev/null | awk '{print $1}')"
        if [[ -n "$got" && "$got" != "$sha" ]]; then
            rm -rf "$tmp"
            fail "Go download failed its checksum (expected $sha, got $got). Not installing it."
        fi
        info "  checksum verified"
    else
        # Say so rather than implying a check happened. An unverifiable download
        # is a decision the operator is entitled to know about.
        info "  ⚠ couldn't fetch go.dev's checksum list — installing UNVERIFIED"
    fi

    local sudo_cmd=""
    [[ -w /usr/local ]] || sudo_cmd="sudo"
    $sudo_cmd rm -rf /usr/local/go
    $sudo_cmd tar -C /usr/local -xzf "$tmp/$file" || { rm -rf "$tmp"; fail "couldn't extract Go into /usr/local"; }
    rm -rf "$tmp"

    # This shell needs it NOW (make setup runs next), and future shells need it
    # too — a Go that works only inside this script would make the next command
    # the operator types fail for no visible reason.
    export PATH="/usr/local/go/bin:$PATH"
    if [[ -d /etc/profile.d ]]; then
        # shellcheck disable=SC2016  # $PATH must stay LITERAL: it expands at login, not now.
        printf 'export PATH=/usr/local/go/bin:$PATH\n' | $sudo_cmd tee /etc/profile.d/go.sh >/dev/null 2>&1 || true
    fi
    command -v go >/dev/null 2>&1 || fail "Go installed to /usr/local/go but is not on PATH — add /usr/local/go/bin and re-run."
    info "  ✓ Go $(go version 2>/dev/null | awk '{print $3}')"
}

if ! command -v go >/dev/null 2>&1; then
    if [[ "$OS" == "Darwin" ]]; then
        info "Installing Go via Homebrew (one-time)…"
        brew install go
    else
        install_go_linux
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
