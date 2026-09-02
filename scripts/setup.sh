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

# Terminal detection, prompting, and sudo pre-authorization. Split into a lib
# so it can be exercised by scripts/lib/tty_test.sh under a real pty — this is
# the logic that broke the fresh-Mac install in #341, and it cannot be tested
# in place because the rest of this file installs Docker and writes /usr/local.
SETUP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/tty.sh
. "$SETUP_DIR/lib/tty.sh"
# shellcheck source=scripts/lib/transcript.sh
. "$SETUP_DIR/lib/transcript.sh"
# Docker Desktop's first launch: probing the CLI without leaking the kernel's
# kill notice, and deciding what to tell an operator watching a screen that may
# have nothing on it. Tested by scripts/lib/dockerwait_test.sh.
# shellcheck source=scripts/lib/dockerwait.sh
. "$SETUP_DIR/lib/dockerwait.sh"
# Idempotent: install.sh already started one and exported DEJIMA_INSTALL_LOG,
# and `exec make setup` inherits both it and the redirected descriptors. This
# call is for the operator who runs `make setup` (or this script) directly.
start_transcript
trap stop_sudo_keepalive EXIT

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
# Tailscale handling — installed early so the DEJIMA_TCP decision later in
# this script and the service install both see an up-to-date state.
#
# Layered behavior:
#   1. Detect tailscale binary; offer install if missing (brew on macOS,
#      curl|sh on Linux). Honored automatically when AUTO_INSTALL_TS=1
#      or stdin is not a TTY (curl|bash flow).
#   2. If installed but not signed in and stdin is a TTY, run `sudo tailscale up`
#      so the user can complete the browser auth in-band. Non-interactive
#      runs skip this; the user can `tailscale up` later.
#   3. After `up` (or if already up), capture the host's tailnet IP and
#      MagicDNS name into ~/.dejima/host.json so clients have a single
#      source of truth to copy.
# ---------------------------------------------------------------------------
bold "1. Tailscale"
TAILSCALE_PRESENT=0
TAILSCALE_RUNNING=0
TAILSCALE_IP=""
TAILSCALE_NAME=""

ts_install_prompt() {
    # Same logic as prompt_yn but keyed off AUTO_INSTALL_TS for clarity.
    if [[ "${AUTO_INSTALL_TS:-}" == "1" ]] || ! have_tty; then
        return 0  # default-yes when there's nobody to ask
    fi
    local reply
    printf '%s [Y/n] ' "$1" >/dev/tty
    read -r -u "$TTY_FD" reply || reply=""
    reply=${reply:-y}
    [[ "$reply" =~ ^[Yy]([Ee][Ss])?$ ]]
}

if command -v tailscale >/dev/null 2>&1; then
    ok "tailscale CLI found"
    TAILSCALE_PRESENT=1
else
    warn "Tailscale not installed"
    info "Tailscale is how Dejima reaches this server from your other devices."
    info "Without it, the daemon listens only on a local Unix socket — fine for"
    info "single-machine use, required for remote access."
    if ts_install_prompt "Install Tailscale now?"; then
        if [[ "$OS" == "Darwin" ]]; then
            if command -v brew >/dev/null 2>&1; then
                info "Running: brew install tailscale"
                if brew install tailscale; then
                    TAILSCALE_PRESENT=1
                    ok "Tailscale installed"
                else
                    fail "brew install tailscale failed"
                fi
            else
                fail "Homebrew not found; install from https://brew.sh then re-run."
            fi
        else
            info "Running: curl -fsSL https://tailscale.com/install.sh | sh"
            prime_sudo "Installing Tailscale"
            if curl -fsSL https://tailscale.com/install.sh | sh; then
                TAILSCALE_PRESENT=1
                ok "Tailscale installed"
            else
                fail "Tailscale install script failed"
            fi
        fi
    else
        info "Skipping Tailscale — daemon will listen on local socket only."
    fi
fi

if [[ "$TAILSCALE_PRESENT" == "1" ]]; then
    # "not signed in" and "the daemon isn't running" are different problems with
    # different fixes, and `tailscale status` fails the same way for both — so
    # `tailscale up` cannot work no matter how many times it is run, and asking
    # for a sudo password first only spends the operator's goodwill on a command
    # that is going to fail. Tell them apart.
    #
    # On macOS the Homebrew FORMULA does ship tailscaled (this said otherwise,
    # and an operator's own `sudo brew services start tailscale` — which listed
    # .../Cellar/tailscale/*/bin/tailscaled and started cleanly — is what
    # corrected it). What the formula does NOT do is start it: nothing runs
    # tailscaled at install time, so the CLI is present and the socket is not.
    # That is one command, and it was being sent to a reinstall instead.
    ts_err=""
    if ! ts_err="$(tailscale status 2>&1 >/dev/null)"; then :; fi
    if tailscale status >/dev/null 2>&1; then
        TAILSCALE_RUNNING=1
        ok "Tailscale is signed in"
    elif [[ "$ts_err" == *"is Tailscale running"* || "$ts_err" == *"failed to connect"* ]]; then
        warn "the tailscale CLI is installed but the Tailscale service isn't running"
        if [[ "$OS" == "Darwin" ]]; then
            info "The Homebrew 'tailscale' formula ships tailscaled but never starts it."
            info "Start it FIRST, then sign in:"
            info "  sudo brew services start tailscale"
            info "  sudo tailscale up --ssh --accept-dns=true"
            info "(Or use the GUI app instead, which manages its own service:"
            info "  brew install --cask tailscale-app   — then open it and sign in.)"
        else
            info "Start the service, then sign in:"
            info "  sudo systemctl enable --now tailscaled && sudo tailscale up"
        fi
        info "Not asking for your password — it wouldn't help until the service is up."
        info "Dejima will finish setting up; the daemon will listen on its local socket."
    else
        warn "Tailscale installed but not signed in"
        if have_tty; then
            info "Running 'sudo tailscale up' — a browser tab opens for sign-in."
            info "(Ctrl-C to skip; you can sign in later with 'sudo tailscale up'.)"
            prime_sudo "Signing in to Tailscale"
            # shellcheck disable=SC2024  # deliberate: sudo should read our terminal
            if sudo tailscale up </dev/tty; then
                # Wait up to 60s for backend to report Running.
                for _ in $(seq 1 60); do
                    if tailscale status >/dev/null 2>&1; then
                        TAILSCALE_RUNNING=1
                        break
                    fi
                    sleep 1
                done
                if [[ "$TAILSCALE_RUNNING" == "1" ]]; then
                    ok "Tailscale is signed in"
                else
                    warn "Tailscale didn't report Running within 60s"
                fi
            else
                warn "'tailscale up' didn't complete — skip for now"
            fi
        else
            info "Non-interactive run — skipping 'tailscale up'."
            info "Sign in later with: sudo tailscale up"
        fi
    fi
fi

if [[ "$TAILSCALE_RUNNING" == "1" ]]; then
    TAILSCALE_IP=$(tailscale ip -4 2>/dev/null | head -1 || true)
    # MagicDNS name: pull the DNSName from `tailscale status --json` without
    # depending on jq. Plain grep/sed is fragile but enough for an info field.
    TAILSCALE_NAME=$(tailscale status --json 2>/dev/null \
        | grep -m1 '"DNSName"' | sed -E 's/.*"DNSName": *"([^"]+)\.?".*/\1/' || true)
    TAILSCALE_NAME="${TAILSCALE_NAME%.}"  # strip trailing dot, if any
    if [[ -n "$TAILSCALE_IP" ]]; then
        mkdir -p "$HOME/.dejima"
        # Single source of truth for "how do my other devices reach this server?"
        cat > "$HOME/.dejima/host.json" <<EOF
{
  "tailscale_ip": "$TAILSCALE_IP",
  "tailscale_name": "$TAILSCALE_NAME",
  "dejima_host": "${TAILSCALE_IP}:7273",
  "captured_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
        ok "wrote $HOME/.dejima/host.json"
        info "Your DEJIMA_HOST is: ${TAILSCALE_IP}:7273"
        [[ -n "$TAILSCALE_NAME" ]] && info "  or by name:        ${TAILSCALE_NAME}:7273"
    fi
fi

# ---------------------------------------------------------------------------
# 2. Docker check
# ---------------------------------------------------------------------------
bold "2. Docker"
if docker_cli_ok; then
    ok "Docker is reachable ($(docker version --format '{{.Server.Version}}' 2>/dev/null || echo 'server unknown'))"
elif command -v docker >/dev/null 2>&1; then
    warn "the 'docker' CLI is on PATH but the daemon isn't reachable"
    # The runtime is installed but stopped — start it for the user instead of
    # dead-ending. Prefer colima (headless-friendly), then OrbStack, then
    # Docker Desktop. Mirrors dejimad's own EnsureDaemon so both agree.
    started=""
    if command -v colima >/dev/null 2>&1; then
        info "Starting colima (cold VM boot takes ~1 min)…"
        colima start && started="colima"
    elif command -v orb >/dev/null 2>&1; then
        info "Starting OrbStack…"
        orb start && started="OrbStack"
    elif [[ "$OS" == "Darwin" && -d /Applications/OrbStack.app ]]; then
        info "Launching OrbStack…"
        open -ga OrbStack && started="OrbStack"
    elif [[ "$OS" == "Darwin" && -d /Applications/Docker.app ]]; then
        info "Launching Docker Desktop…"
        open -ga Docker && started="Docker Desktop"
    elif [[ "$OS" == "Linux" ]] && command -v systemctl >/dev/null 2>&1; then
        info "Starting docker via systemd…"
        sudo systemctl start docker && started="docker (systemd)"
    fi

    if [[ -z "$started" ]]; then
        fail "couldn't find a runtime to start (colima / OrbStack / Docker Desktop)"
        info "Start your Docker runtime manually, then re-run scripts/setup.sh"
        exit 1
    fi

    info "Waiting up to 90s for the Docker daemon to come up…"
    for _ in $(seq 1 90); do
        if docker_cli_ok; then break; fi
        sleep 1
    done
    if docker_cli_ok; then
        ok "Docker is now reachable (via $started)"
    else
        fail "$started started but \`docker version\` is still not reachable after 90s"
        info "Check the runtime's status and re-run scripts/setup.sh"
        exit 1
    fi
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
                    # The single quotes below are load-bearing, and SC2016's
                    # "did you mean to expand?" is exactly backwards here. In
                    # double quotes the shell RUNS what's inside backticks: this
                    # line used to delete the user's colima VM as a side effect
                    # of printing advice about deleting it, then print the
                    # sentence with a hole where the command name had been.
                    # shellcheck disable=SC2016
                    colima start || {
                        fail "colima start failed"
                        info 'Try `colima delete && colima start` to reset, then re-run scripts/setup.sh.'
                        exit 1
                    }
                    # shellcheck disable=SC2016
                    if docker_cli_ok; then
                        ok "Docker is now reachable (via colima)"
                    else
                        fail 'colima started but `docker version` is not reachable'
                        info 'Check `colima status` and re-run scripts/setup.sh.'
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
                    info "Running: brew install --cask docker-desktop"
                    prime_sudo "Installing Docker Desktop"
                    with_tty brew install --cask docker-desktop || {
                        stop_sudo_keepalive
                        fail "brew install --cask docker-desktop failed (Gatekeeper or a previous install may be involved)"
                        info "If install dropped to /Applications/Docker.app, just open it once to finish setup."
                        info "Otherwise: download from https://www.docker.com/products/docker-desktop/ and re-run setup."
                        exit 1
                    }
                    stop_sudo_keepalive
                    # A freshly-installed Docker Desktop has no daemon until it
                    # has been launched once: first run installs a privileged
                    # helper and puts a licence screen in front of the user. This
                    # step used to *tell* the operator to open it and then count
                    # silently to 90 without opening anything — which on a fresh
                    # Mac mini timed out and exited 1 while Docker sat waiting to
                    # be clicked. Open it, and allow for a human in the loop.
                    info "Launching Docker Desktop — accept its licence prompt if one appears."
                    if ! open -a Docker 2>/dev/null; then
                        warn "couldn't launch Docker Desktop automatically"
                        info "Open /Applications/Docker.app by hand and leave this running."
                        info "If macOS refuses with error -10673, the app is quarantined:"
                        info "  xattr -dr com.apple.quarantine /Applications/Docker.app"
                    fi
                    info "Waiting for the Docker daemon (up to 5 min; first launch is slow)…"
                    if docker_wait_for_daemon 300; then
                        ok "Docker is now reachable"
                    else
                        fail "Docker still not reachable after 5 minutes"
                        if docker_desktop_running; then dd_running=1; else dd_running=0; fi
                        while IFS= read -r line; do
                            info "$line"
                        done < <(docker_wait_advice "$dd_running" "$(console_user)" "$(whoami)")
                        info ""
                        info "Then re-run the installer — it is idempotent and picks up here:"
                        info "  curl -fsSL https://dejima.tech/install.sh | bash"
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
        # Linux used to dead-end here: the entire install-Docker path was behind
        # `$OS == Darwin`, so a Linux user got a list of distro commands and
        # exit 1 — on the one channel (a cloud VPS) where the machine is most
        # likely to be brand new. Offer it, exactly as macOS does.
        info "Dejima runs each island in a container, so it needs a container runtime."
        info "Docker's official install script supports Debian/Ubuntu, Fedora, CentOS"
        info "and derivatives, and picks the right packages for this distro."
        info ""
        if prompt_yn "Install Docker now via https://get.docker.com?" "y"; then
            prime_sudo "Installing Docker"
            info "Running: curl -fsSL https://get.docker.com | sh"
            if ! curl -fsSL https://get.docker.com | sh; then
                fail "the Docker install script failed"
                info "Install it with your package manager and re-run:"
                info "  Debian/Ubuntu: sudo apt install docker.io"
                info "  Arch:          sudo pacman -S docker"
                info "  Fedora:        sudo dnf install docker"
                exit 1
            fi
            if command -v systemctl >/dev/null 2>&1; then
                info "Enabling and starting the docker service…"
                sudo systemctl enable --now docker || warn "couldn't enable docker via systemd"
            fi
            # Without this, every later docker call needs sudo — including the
            # daemon's, which does not run as root. Takes effect on the next
            # login, so the check below deliberately still uses sudo.
            if ! id -nG "$USER" 2>/dev/null | tr ' ' '\n' | grep -qx docker; then
                info "Adding $USER to the 'docker' group…"
                sudo usermod -aG docker "$USER" || warn "couldn't add $USER to the docker group"
                info "  (group membership applies at your NEXT login)"
            fi
            if docker version >/dev/null 2>&1; then
                ok "Docker is now reachable"
            elif sudo docker version >/dev/null 2>&1; then
                ok "Docker is installed and running"
                warn "reachable only via sudo until you log out and back in"
                info "The group change above is what fixes that. Log out and back in,"
                info "then re-run the installer — it is idempotent and picks up here:"
                info "  curl -fsSL https://dejima.tech/install.sh | bash"
                exit 1
            else
                fail "Docker installed but the daemon isn't reachable"
                info "Start it, then re-run: sudo systemctl start docker"
                exit 1
            fi
        else
            info "Install your preferred container runtime, then re-run scripts/setup.sh"
            info "  Debian/Ubuntu: sudo apt install docker.io"
            info "  Arch:          sudo pacman -S docker"
            info "  Fedora:        sudo dnf install docker"
            exit 1
        fi
    fi
fi

# ---------------------------------------------------------------------------
# 2. Go check + build
# ---------------------------------------------------------------------------
bold "3. Build binaries"
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
# NB: no `make build` here. The install target below depends on build, and the
# build targets are .PHONY with no file prerequisites, so make re-runs them
# unconditionally — calling both meant four `go build` invocations per install
# and the user watching the "Build binaries" work repeat under "Install".
ok "Go toolchain ready"

# ---------------------------------------------------------------------------
# 3. Install to /usr/local/bin (or $PREFIX)
# ---------------------------------------------------------------------------
bold "4. Build & install binaries"
# `make install` falls back to `sudo install` when the prefix isn't writable, and
# /usr/local/bin is root-owned on Apple Silicon. Prime first so the prompt lands
# here with a reason attached instead of partway through make's own output, where
# an unexplained `Password:` reads as a hang.
if [[ ! -w "${PREFIX:-/usr/local}/bin" ]]; then
    prime_sudo "Installing dejima + dejimad to ${PREFIX:-/usr/local}/bin"
fi
with_tty make install
ok "installed dejima + dejimad"

# ---------------------------------------------------------------------------
# 4. Island image
# ---------------------------------------------------------------------------
bold "5. Island image"
if docker image inspect dejima/island:latest >/dev/null 2>&1; then
    ok "dejima/island:latest already built"
else
    info "Building dejima/island:latest (5+ minutes on a cold cache)…"
    make image
    ok "image ready"
fi

# ---------------------------------------------------------------------------
# Remote access: expose the Tailscale-pinned TCP listener so other devices
# (your laptop, phone) can reach this host with DEJIMA_HOST. Only tailnet peers
# are accepted. Defaults on when Tailscale is present; override with
# DEJIMA_TCP="" to keep the daemon local-socket-only, or DEJIMA_TCP=":1234".
# ---------------------------------------------------------------------------
DEJIMA_TCP="${DEJIMA_TCP-__unset__}"
if [[ "$DEJIMA_TCP" == "__unset__" ]]; then
    # Step 1 already ran the detect+install+up flow, so we can trust the flag.
    if [[ "$TAILSCALE_RUNNING" == "1" ]]; then
        DEJIMA_TCP=":7273"
    else
        DEJIMA_TCP=""
    fi
fi

# ---------------------------------------------------------------------------
# 6. Service install
# ---------------------------------------------------------------------------
if [[ "${SKIP_SERVICE:-}" == "1" ]]; then
    bold "6. Service install (skipped via SKIP_SERVICE=1)"
else
    bold "6. Service install"
    install_args=()
    [[ -n "$DEJIMA_TCP" ]] && install_args+=(--tcp "$DEJIMA_TCP")
    [[ -n "${NOTIFY_URL:-}" ]] && install_args+=(--notify "$NOTIFY_URL")
    dejima service install ${install_args[@]+"${install_args[@]}"}
fi

# ---------------------------------------------------------------------------
# 5b. Ensure dejimad is actually reachable
# ---------------------------------------------------------------------------
# On headless macOS (no Aqua/GUI session), launchctl can't load the plist —
# `dejima service install` writes the plist and warns but exits 0. In that
# case the daemon isn't running yet. Start it manually as a fallback so the
# rest of the flow works for this session.
if ! dejima doctor 2>/dev/null | grep -q "daemon.*OK"; then
    if pgrep -f "/usr/local/bin/dejimad" >/dev/null 2>&1; then
        info "dejimad already running (not via launchd; won't persist across reboots)"
    else
        warn "daemon not reachable — starting it manually as a fallback"
        info "  (this session only; see service-install warning above for persistence)"
        mkdir -p "$HOME/Library/Logs/dejima"
        if [[ -n "$DEJIMA_TCP" ]]; then
            nohup /usr/local/bin/dejimad --tcp "$DEJIMA_TCP" \
                > "$HOME/Library/Logs/dejima/dejimad.out.log" \
                2> "$HOME/Library/Logs/dejima/dejimad.err.log" < /dev/null &
        else
            nohup /usr/local/bin/dejimad \
                > "$HOME/Library/Logs/dejima/dejimad.out.log" \
                2> "$HOME/Library/Logs/dejima/dejimad.err.log" < /dev/null &
        fi
        disown
        # Give it a moment to come up
        for _ in 1 2 3 4 5 6 7 8 9 10; do
            if dejima doctor 2>/dev/null | grep -q "daemon.*OK"; then
                break
            fi
            sleep 1
        done
    fi
fi

# ---------------------------------------------------------------------------
# 7. Final check
# ---------------------------------------------------------------------------
bold "7. Health check"
dejima doctor || true

printf '\n'
bold "Setup complete."
info "Try:  dejima init --repo git@github.com:you/repo.git"
info "Then: dejima connect <name>"

# The address is the one piece of state that has to travel from this machine to
# every other device, and there is no way to look it up from the other end. It
# used to print only when Tailscale was up, so a run where Tailscale failed
# ended on "Setup complete" with the remote-access section silently absent —
# nothing told the operator that the thing they need was missing, or why.
printf '\n'
bold "To drive this server from another device:"
if [[ -n "$TAILSCALE_IP" ]]; then
    info "Install the dejima client on the other machine, then set:"
    printf '\n'
    bold "    export DEJIMA_HOST=${TAILSCALE_IP}:7273"
    [[ -n "$TAILSCALE_NAME" ]] && info "    (or by name: ${TAILSCALE_NAME}:7273)"
    printf '\n'
    info "That line is the whole handoff — copy it now. Also saved to"
    info "$HOME/.dejima/host.json, and printed by 'dejima doctor' on this host."
else
    warn "no remote address yet — Tailscale isn't up on this machine."
    info "Dejima is installed and working LOCALLY; this only affects reaching it"
    info "from your laptop or phone. Nothing here needs redoing."
    info ""
    info "To finish it: bring Tailscale up (see the Tailscale step above), then"
    info "run 'dejima doctor' here — it prints the DEJIMA_HOST to copy."
fi
