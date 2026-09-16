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
# ASKED FIRST, before Go, Docker, or a clone. Local and Host both build the
# server stack here; Client is a ~15MB CLI from install-client.sh and nothing
# else, so it stops this script rather than running it.
#
# It used to be one road. Someone who only wanted to drive a server elsewhere
# was walked through a Go toolchain, a Homebrew Docker Desktop install, an image
# build and a launchd service — with no signpost that a one-minute script
# existed. Every failure they could hit was in work they never needed to do, and
# a fresh-Mac install failed three times in the field that way.
# THREE answers, not two, and they are the SAME THREE scripts/setup.sh asks for.
# This script used to offer SERVER or CLIENT while setup.sh — which it execs at
# the end, via `make setup` — asked again with Local / Host / Client. An operator
# answered "1 SERVER", waited through a Go toolchain and a clone, and was then
# asked a different question with different words and a third option that had not
# been on the menu. dejima.tech documents `DEJIMA_SETUP=local` against THIS url,
# and this script did not read that variable at all: the local answer survived
# only because the env var rode through `make` to setup.sh, which did read it.
# So the wrong menu was cosmetic in that one path and load-bearing in every other.
#
# Asking once and passing the answer down is what makes the two agree. DEST is
# exported below so setup_destination() short-circuits instead of re-asking.
DEST=""
case "${DEJIMA_SETUP:-}" in
    local|host|client) DEST="${DEJIMA_SETUP}" ;;
    "") ;;
    # Same strictness as scripts/lib/dest.sh, for the same reason: a caller that
    # spells out its intent and typos it must not silently get a host install.
    # No explicit `exit` needed here, unlike dest.sh — that copy of fail() only
    # prints, this one exits. Worth stating because the two look identical.
    *) fail "DEJIMA_SETUP must be local, host or client (got '${DEJIMA_SETUP}')" ;;
esac
# DEJIMA_ROLE is the older spelling and stays working. Its historical contract
# was "any non-empty value means don't ask" — which always meant server — so an
# unrecognised value keeps landing on host rather than becoming a hard failure:
# the runbooks and CI jobs setting it never agreed on a vocabulary, and breaking
# them to tidy one up would be a worse trade than honouring what they meant.
if [[ -z "$DEST" && -n "${DEJIMA_ROLE:-}" ]]; then
    case "${DEJIMA_ROLE}" in
        local|LOCAL)   DEST="local" ;;
        client|CLIENT) DEST="client" ;;
        *)             DEST="host" ;;
    esac
fi

# /dev/tty, not stdin: `curl … | bash` makes stdin a pipe while a person watches
# from the keyboard, which is exactly the misreading that produced #341. Piped
# and non-interactive runs fall through to the host path, which is what an
# unattended installer should do — and what every runbook piping this expects.
# `[[ -e /dev/tty ]]` is the WRONG test and this is the third variant of that
# mistake this week: the device node exists on a process with no controlling
# terminal, and opening it then fails with ENXIO. Probe by OPENING it, in a
# subshell so a failed redirect is not fatal under `set -e` — which is exactly
# what scripts/lib/tty.sh does, for exactly this reason. That library is not
# available here; the repo has not been cloned yet.
if [[ -z "$DEST" ]]; then
    if ( exec </dev/tty ) 2>/dev/null; then
        echo
        bold "How do you want to run Dejima on this machine?"
        echo
        info "    l) Local  — just for you, on this machine"
        info "               (Docker + the daemon; no tailnet, nothing always-on)"
        info "    h) Host   — an always-on server for a team, reachable from your other devices"
        info "               (adds Tailscale; this machine stays awake)"
        info "    c) Client — a server already exists and you have an invite"
        info "               (no build needed — you want the CLI from a package manager)"
        echo
        printf '  Choice [L/h/c]: '
        read -r reply </dev/tty 2>/dev/null || reply=""
        # Bare Enter is Local: the least destructive answer, and the one an
        # operator who did not read the menu most likely wanted. The digits are
        # accepted because older docs and this script's own previous menu said
        # "[1/2]", and someone following one of those should not be punished for
        # it — 2 meant CLIENT there, which is why it is not h.
        case "${reply:-l}" in
            h|H|host|HOST)     DEST="host" ;;
            c|C|client|CLIENT) DEST="client" ;;
            2)                 DEST="client" ;;
            *)                 DEST="local" ;;
        esac
    else
        DEST="host"
    fi
fi

if [[ "$DEST" == "client" ]]; then
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
fi

# A machine that was a CLIENT keeps `export DEJIMA_HOST=…` in its shell rc, put
# there by install-client.sh. Install a local daemon on top of that and the CLI
# still talks to the old server: the new daemon is running, `dejima ls` is empty
# or someone else's, and nothing says why. Detected and named rather than edited
# — an rc file is the operator's, and a sed through it is how an installer eats
# a line somebody wrote by hand.
if [[ "$DEST" == "local" ]]; then
    stale_rc=""
    for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.profile" "$HOME/.zshenv"; do
        [[ -f "$rc" ]] && grep -q '^[^#]*DEJIMA_HOST' "$rc" 2>/dev/null && stale_rc="${stale_rc} $rc"
    done
    if [[ -n "${DEJIMA_HOST:-}" || -n "$stale_rc" ]]; then
        echo
        bold "Heads up: this machine is already pointed at a Dejima server."
        [[ -n "${DEJIMA_HOST:-}" ]] && info "  DEJIMA_HOST=${DEJIMA_HOST} is set in this shell"
        [[ -n "$stale_rc" ]] && info "  and set in:${stale_rc}"
        info "A local daemon will start, but the CLI will keep talking to that server"
        info "until you remove the line and open a new shell. Nothing here edits it."
        echo
    fi
fi

# Hand the answer to setup.sh so it does not ask a second question. This is the
# whole reason the fork moved here rather than being duplicated.
export DEJIMA_SETUP="$DEST"

# Say which of the three this run is. The operator has just answered a question
# whose consequences arrive ten minutes later (Tailscale or not, always-on or
# not), and an installer that never repeats the answer back gives them nothing
# to check it against. It is also the line the fork tests assert on: a signal
# emitted right after the decision, rather than a downstream side effect that
# only appears if the whole build gets that far.
info "install type: $DEST"
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
