#!/usr/bin/env bash
# Dejima client installer — drops the `dejima` CLI on this machine from a
# published GitHub Release. For laptops/desktops that drive a *remote* daemon;
# no Go, no Docker, no daemon. For the full server stack, use install.sh instead.
#
# Usage:
#   curl -fsSL https://aoos.github.io/dejima/install-client.sh | bash
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

tar -xzf "$tmp/$asset" -C "$tmp"

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
cat <<EOS

Next:
  export DEJIMA_HOST=your-host.tailnet:7273   # your daemon's address
  dejima                                       # opens the dashboard
  dejima connect <island>                      # attach to an agent

(No daemon installed here — this is the client. Run the full server with
 install.sh on the host.)
EOS
