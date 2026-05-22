#!/usr/bin/env bash
# Dejima first-run bootstrap.
#
# Walks a fresh host through: Docker check → optional OrbStack install →
# build → install → island image → service install. Idempotent; safe to
# re-run after a partial setup.
#
# Usage:
#   scripts/setup.sh                   # interactive
#   NOTIFY_URL=https://… scripts/setup.sh   # auto-subscribe a webhook at the end
#   AUTO_INSTALL_DOCKER=1 scripts/setup.sh  # don't prompt; install Docker if missing
#   SKIP_SERVICE=1 scripts/setup.sh         # build only; don't install as a service

set -euo pipefail

# ---------------------------------------------------------------------------
# Terminal helpers
# ---------------------------------------------------------------------------
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }
info()   { printf '  %s\n' "$*"; }
ok()     { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn()   { printf '  \033[33m!\033[0m %s\n' "$*"; }
fail()   { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; }

prompt_yn() {
    local prompt="$1"
    local default="${2:-y}"
    if [[ "${AUTO_INSTALL_DOCKER:-}" == "1" || ! -t 0 ]]; then
        # Non-interactive: honor the default.
        [[ "$default" == "y" ]]
        return $?
    fi
    local reply
    read -r -p "$prompt [Y/n] " reply
    reply=${reply:-$default}
    case "$reply" in
        [Yy]|[Yy][Ee][Ss]) return 0 ;;
        *) return 1 ;;
    esac
}

OS=$(uname -s)
case "$OS" in
    Darwin) ;;
    Linux)  ;;
    *)
        fail "unsupported OS: $OS (Dejima v1 supports macOS and Linux)"
        exit 1
        ;;
esac

# ---------------------------------------------------------------------------
# 1. Docker check
# ---------------------------------------------------------------------------
bold "1. Docker"
if docker version >/dev/null 2>&1; then
    ok "Docker is reachable ($(docker version --format '{{.Server.Version}}' 2>/dev/null || echo 'server unknown'))"
elif command -v docker >/dev/null 2>&1; then
    warn "the 'docker' CLI is on PATH but the daemon isn't reachable"
    info "Start your runtime (OrbStack / Docker Desktop / colima) and re-run scripts/setup.sh"
    exit 1
else
    warn "Docker is not installed"
    if [[ "$OS" == "Darwin" ]]; then
        if command -v brew >/dev/null 2>&1; then
            # SSH context detection — Docker Desktop and OrbStack both require
            # a GUI first-launch click. From SSH, the user can't grant that
            # permission; colima is the only sane default for headless setups.
            is_ssh=0
            if [[ -n "${SSH_CONNECTION:-}" || -n "${SSH_TTY:-}" ]]; then
                is_ssh=1
            fi

            if [[ "$is_ssh" == "1" ]]; then
                info "You're connected over SSH — Docker Desktop and OrbStack need a GUI"
                info "first-launch click that you can't grant remotely. Recommending colima"
                info "(CLI-only, OSS, no GUI needed)."
                info ""
                info "If you'd rather use Docker Desktop, exit and either:"
                info "  • VNC into this Mac and open /Applications/Docker.app, OR"
                info "  • Install Docker Desktop physically at the Mac mini's console."
                info ""
                if prompt_yn "Install colima + docker CLI now via Homebrew?" "y"; then
                    info "Running: brew install colima docker"
                    brew install colima docker || {
                        fail "brew install colima docker failed"
                        exit 1
                    }
                    info "Starting colima (downloads a small Linux VM the first time; ~1 min)…"
                    colima start || {
                        fail "colima start failed"
                        info "Try `colima delete && colima start` to reset, then re-run scripts/setup.sh."
                        exit 1
                    }
                    if docker version >/dev/null 2>&1; then
                        ok "Docker is now reachable (via colima)"
                    else
                        fail "colima started but `docker version` is not reachable"
                        info "Check `colima status` and re-run scripts/setup.sh."
                        exit 1
                    fi
                else
                    info "Set up your preferred Docker runtime, then re-run scripts/setup.sh"
                    exit 1
                fi
            else
                info "Docker Desktop is the recommended default: free for personal use and"
                info "small businesses (<250 employees AND <\$10M revenue), familiar everywhere."
                info ""
                info "Alternatives, if you'd rather install one of these yourself first:"
                info "  • OrbStack  — faster on Apple Silicon, but free tier is personal/non-commercial only"
                info "                (brew install --cask orbstack)"
                info "  • colima    — CLI-only, OSS, free for any use"
                info "                (brew install colima docker  →  colima start)"
                info ""
                if prompt_yn "Install Docker Desktop now via Homebrew?" "y"; then
                    info "Running: brew install --cask docker"
                    brew install --cask docker || {
                        fail "brew install --cask docker failed (Gatekeeper or a previous install may be involved)"
                        info "If install dropped to /Applications/Docker.app, just open it once to finish setup."
                        info "Otherwise: download from https://www.docker.com/products/docker-desktop/ and re-run setup."
                        exit 1
                    }
                    info "Now open /Applications/Docker.app once to grant macOS permissions."
                    info "Setup will wait up to 90s for the Docker daemon to come up."
                    for _ in $(seq 1 90); do
                        if docker version >/dev/null 2>&1; then
                            ok "Docker is now reachable"
                            break
                        fi
                        sleep 1
                    done
                    if ! docker version >/dev/null 2>&1; then
                        fail "Docker still not reachable after 90s"
                        info "Launch Docker Desktop manually, then re-run scripts/setup.sh"
                        exit 1
                    fi
                else
                    info "Install your preferred Docker runtime, then re-run scripts/setup.sh"
                    exit 1
                fi
            fi
        else
            fail "Homebrew not found and Docker not installed"
            info "Install Homebrew first (https://brew.sh), then re-run scripts/setup.sh"
            info "Or download Docker Desktop directly from https://www.docker.com/products/docker-desktop/"
            exit 1
        fi
    else
        fail "Docker / Podman not installed"
        info "On Linux: install docker via your distro's package manager, then re-run."
        info "  Debian/Ubuntu: sudo apt install docker.io"
        info "  Arch:          sudo pacman -S docker"
        info "  Fedora:        sudo dnf install docker"
        exit 1
    fi
fi

# ---------------------------------------------------------------------------
# 2. Go check + build
# ---------------------------------------------------------------------------
bold "2. Build binaries"
if ! command -v go >/dev/null 2>&1; then
    fail "Go is not installed"
    if [[ "$OS" == "Darwin" ]] && command -v brew >/dev/null 2>&1; then
        info "Install Go via Homebrew: brew install go"
    else
        info "Install Go from https://go.dev/dl/"
    fi
    exit 1
fi
ok "Go: $(go version | awk '{print $3}')"
make build
ok "built bin/dejima, bin/dejimad"

# ---------------------------------------------------------------------------
# 3. Install to /usr/local/bin (or $PREFIX)
# ---------------------------------------------------------------------------
bold "3. Install binaries"
make install
ok "installed dejima + dejimad"

# ---------------------------------------------------------------------------
# 4. Island image
# ---------------------------------------------------------------------------
bold "4. Island image"
if docker image inspect dejima/island:latest >/dev/null 2>&1; then
    ok "dejima/island:latest already built"
else
    info "Building dejima/island:latest (5+ minutes on a cold cache)…"
    make image
    ok "image ready"
fi

# ---------------------------------------------------------------------------
# 5. Service install
# ---------------------------------------------------------------------------
if [[ "${SKIP_SERVICE:-}" == "1" ]]; then
    bold "5. Service install (skipped via SKIP_SERVICE=1)"
else
    bold "5. Service install"
    if [[ -n "${NOTIFY_URL:-}" ]]; then
        dejima service install --notify "$NOTIFY_URL"
    else
        dejima service install
    fi
fi

# ---------------------------------------------------------------------------
# 6. Final check
# ---------------------------------------------------------------------------
bold "6. Health check"
dejima doctor || true

printf '\n'
bold "Setup complete."
info "Try:  dejima init --repo git@github.com:you/repo.git"
info "Then: dejima connect <name>"
