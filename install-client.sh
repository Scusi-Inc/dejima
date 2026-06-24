#!/usr/bin/env bash
# Dejima client installer — drops the `dejima` CLI on this machine from a
# published GitHub Release. For laptops/desktops that drive a *remote* daemon;
# no Go, no Docker, no daemon. For the full server stack, use install.sh instead.
#
# Usage:
#   curl -fsSL https://dejima.tech/install-client.sh | bash
#
# Knobs:
#   DEJIMA_VERSION   release tag to install (default: latest, e.g. v0.1.0)
#   PREFIX           install prefix (default: /usr/local) → $PREFIX/bin
#
# NOTE: until the darwin binaries are notarized, macOS quarantines downloads;
# this script strips the quarantine attribute from the installed binary.

set -euo pipefail

REPO="aoos/dejima"
PREFIX="${PREFIX:-/usr/local}"
BIN_DIR="$PREFIX/bin"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
fail() { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

bold "Dejima client installer"

# --- detect platform ------------------------------------------------------
os=$(uname -s)
case "$os" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) fail "unsupported OS: $os (the daemon is Unix-only; the client supports macOS/Linux here, Windows via Releases)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported arch: $arch" ;;
esac
info "platform: ${os}/${arch}"

# --- resolve version ------------------------------------------------------
ver="${DEJIMA_VERSION:-}"
if [[ -z "$ver" ]]; then
  info "resolving latest release…"
  ver=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  [[ -n "$ver" ]] || fail "no published releases yet — set DEJIMA_VERSION, or build from source via install.sh"
fi
info "version:  $ver"

asset="dejima_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${ver}"

# --- download + verify ----------------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
info "downloading $asset"
curl -fsSL "$base/$asset" -o "$tmp/$asset" || fail "download failed: $base/$asset"

if curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" 2>/dev/null; then
  want=$(grep " $asset\$" "$tmp/SHA256SUMS" | awk '{print $1}')
  if [[ -n "$want" ]]; then
    if command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
    else
      got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
    fi
    [[ "$want" == "$got" ]] || fail "checksum mismatch for $asset (want $want, got $got)"
    info "checksum OK"
  fi
else
  info "warning: SHA256SUMS not found; skipping checksum verification"
fi

tar -xzf "$tmp/$asset" -C "$tmp" || fail "couldn't unpack $asset (corrupt download?) — re-run to retry."
[[ -f "$tmp/dejima" ]] || fail "the downloaded archive didn't contain a 'dejima' binary — re-run to retry, or report this at https://github.com/${REPO}/issues."

# --- install (client only) ------------------------------------------------
install_bin() {
  if [[ -w "$BIN_DIR" ]]; then
    install -m 0755 "$tmp/dejima" "$BIN_DIR/dejima"
  elif mkdir -p "$BIN_DIR" 2>/dev/null && [[ -w "$BIN_DIR" ]]; then
    install -m 0755 "$tmp/dejima" "$BIN_DIR/dejima"
  else
    info "writing to $BIN_DIR needs sudo"
    sudo install -d -m 0755 "$BIN_DIR"
    sudo install -m 0755 "$tmp/dejima" "$BIN_DIR/dejima"
  fi
}
install_bin

# Strip macOS quarantine (binaries are unsigned until notarization lands).
if [[ "$os" == "darwin" ]]; then
  xattr -d com.apple.quarantine "$BIN_DIR/dejima" 2>/dev/null || true
fi

bold "Installed dejima $ver → $BIN_DIR/dejima"

# ---------------------------------------------------------------------------
# Tailscale: this is the network the client uses to reach the daemon, so we
# offer to set it up alongside the CLI. Detection and prompts mirror the
# server flow in scripts/setup.sh.
# ---------------------------------------------------------------------------
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }

ts_prompt_yn() {
    if [[ "${AUTO_INSTALL_TS:-}" == "1" || ! -t 0 ]]; then
        return 0
    fi
    local reply
    read -r -p "$1 [Y/n] " reply </dev/tty 2>/dev/null || return 0
    reply=${reply:-y}
    [[ "$reply" =~ ^[Yy]([Ee][Ss])?$ ]]
}

printf '\n'
bold "Tailscale"
if command -v tailscale >/dev/null 2>&1; then
    ok "tailscale CLI found"
else
    warn "Tailscale not installed"
    info "Tailscale is the network this client uses to reach your Dejima server."
    if ts_prompt_yn "Install Tailscale now?"; then
        if [[ "$os" == "darwin" ]]; then
            if command -v brew >/dev/null 2>&1; then
                info "Running: brew install tailscale"
                brew install tailscale && ok "Tailscale installed" || warn "brew install tailscale failed"
            else
                warn "Homebrew not found — install from https://brew.sh, then 'brew install tailscale'"
            fi
        else
            info "Running: curl -fsSL https://tailscale.com/install.sh | sh"
            curl -fsSL https://tailscale.com/install.sh | sh && ok "Tailscale installed" || warn "Tailscale install failed"
        fi
    fi
fi

if command -v tailscale >/dev/null 2>&1; then
    if ! tailscale status >/dev/null 2>&1; then
        warn "Tailscale not signed in"
        if [[ -t 0 || -e /dev/tty ]]; then
            info "Running 'sudo tailscale up' — a browser tab opens for sign-in."
            info "(Ctrl-C to skip; sign in later with 'sudo tailscale up'.)"
            sudo tailscale up </dev/tty 2>/dev/tty || warn "'tailscale up' didn't complete"
        else
            info "Non-interactive run — sign in later with: sudo tailscale up"
        fi
    else
        ok "Tailscale is signed in"
    fi
fi

# ---------------------------------------------------------------------------
# DEJIMA_HOST: prompt for the server's tailnet address, validate via TCP
# probe, and persist to the shell rc so future shells pick it up.
# ---------------------------------------------------------------------------
printf '\n'
bold "Server address"
info "On the SERVER (mac mini / linux box), run 'tailscale ip -4' to find its address."
info "Example: 100.84.12.7"

server_host=""
if [[ -e /dev/tty && -z "${DEJIMA_HOST_PREFILL:-}" ]]; then
    # Default port assumed below; user types IP or hostname only.
    read -r -p "Enter your server's tailnet IP or hostname (blank to skip): " server_host </dev/tty 2>/dev/null || true
elif [[ -n "${DEJIMA_HOST_PREFILL:-}" ]]; then
    server_host="$DEJIMA_HOST_PREFILL"
fi

if [[ -n "$server_host" ]]; then
    # Strip a user-supplied port, then re-attach the canonical 7273.
    server_host="${server_host%:*}"
    candidate_host="${server_host}:7273"
    info "Probing $candidate_host…"
    probe_ok=0
    if command -v nc >/dev/null 2>&1; then
        if nc -z -w3 "$server_host" 7273 >/dev/null 2>&1; then probe_ok=1; fi
    else
        # Fallback: bash's /dev/tcp (works on macOS bash too)
        if (exec 3<>/dev/tcp/$server_host/7273) 2>/dev/null; then
            probe_ok=1
            exec 3<&-
            exec 3>&-
        fi
    fi
    if [[ "$probe_ok" == "1" ]]; then
        ok "reached $candidate_host"
    else
        warn "couldn't reach $candidate_host — saving anyway"
        info "  (server may be down, or Tailscale not connected yet)"
    fi

    # Pick the most appropriate rc file: zsh on macOS, bash on Linux.
    rc=""
    case "$os" in
        darwin) rc="$HOME/.zshenv" ;;
        linux)  rc="$HOME/.bashrc" ;;
    esac
    line="export DEJIMA_HOST=$candidate_host"
    if [[ -n "$rc" ]] && ! grep -qxF "$line" "$rc" 2>/dev/null; then
        printf '\n# Added by dejima install-client.sh\n%s\n' "$line" >> "$rc"
        ok "appended DEJIMA_HOST to $rc"
    fi
    info "Set for this shell:  export DEJIMA_HOST=$candidate_host"
fi

cat <<EOS

Next:
  export DEJIMA_HOST=${candidate_host:-your-host:7273}   # if not already set above
  dejima                                                  # opens the dashboard
  dejima connect <island>                                 # attach to an agent

(No daemon installed here — this is the client. Run the full server with
 install.sh on the host.)
EOS
