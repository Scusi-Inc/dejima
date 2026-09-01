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
info "Installs the dejima CLI (~15 MB) and offers to set up Tailscale. ~1 minute."
info "A couple of steps will pause to ASK you something — watch for the prompts below."

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
# An ~9 MB asset over a flaky link is where this installer is most likely to
# fail, and it's the first thing a new user runs. A real report: five straight
# `curl: (56) Connection died` on a Mac, while the asset itself was fine
# (checksum verified, 3s from elsewhere). So don't take one failure as final.
#
#   --retry/--retry-all-errors  ride out transient drops (56 is a mid-transfer
#                               death, not a bad URL — a 404 still fails fast
#                               because -f makes it a hard error curl won't retry)
#   -C -                        resume rather than restart from zero
#   pass 2: -4                  a broken IPv6 path to the CDN is the most common
#                               cause of a repeatedly-dying transfer
#   pass 3: --http1.1           HTTP/2 stream resets are the next most common
#
# Show a progress bar (-#) instead of the silent -s: a multi-MB download over a
# slow link is the longest single step, and a blank terminal reads as a hang.
# Fall back to a quiet download if this isn't a terminal (piped/CI logs).
if [[ -t 2 ]]; then
  curl_out=(--progress-bar)
else
  curl_out=(-sS)
fi

# --retry-all-errors landed in curl 7.71 (2020). An unknown option makes curl
# exit immediately, which would turn a hardening measure into a hard failure on
# an older box — so probe for it once instead of assuming.
retry_all=()
if curl --help all 2>/dev/null | grep -q -- --retry-all-errors; then
  retry_all=(--retry-all-errors)
fi

fetch_asset() {
  # $@ = extra curl args for this attempt
  curl -fL "${curl_out[@]}" --retry 3 --retry-delay 2 "${retry_all[@]}" \
       --connect-timeout 20 -C - "$@" "$base/$asset" -o "$tmp/$asset"
}

if ! fetch_asset; then
  info "download died mid-transfer — retrying over IPv4 only…"
  # A partial file from the failed attempt confuses `-C -` if the next attempt
  # negotiates differently; start each fallback clean.
  rm -f "$tmp/$asset"
  if ! fetch_asset -4; then
    info "still failing — retrying with HTTP/1.1…"
    rm -f "$tmp/$asset"
    if ! fetch_asset -4 --http1.1; then
      fail "download failed after 3 attempts (plain, IPv4, HTTP/1.1): $base/$asset
  The release asset is almost certainly fine — this is the network path to
  GitHub's CDN. Check whether you're on a VPN or exit node, then either re-run
  this installer or grab it by hand:
    curl -4 -fL -o /tmp/$asset $base/$asset
    tar -xzf /tmp/$asset -C /tmp && sudo install -m 0755 /tmp/dejima /usr/local/bin/dejima"
    fi
  fi
fi

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
    read -r -p "▸ $1 [Y/n] " reply </dev/tty 2>/dev/null || return 0
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
                if brew install tailscale; then
                    ok "Tailscale installed"
                    # The formula ships tailscaled but never starts it, so
                    # `tailscale up` would fail with "is Tailscale running?" —
                    # installed, and useless, with nothing saying why.
                    info "Starting the Tailscale service (needs your password)…"
                    sudo brew services start tailscale >/dev/null 2>&1 \
                        && ok "Tailscale service started" \
                        || warn "couldn't start it — run: sudo brew services start tailscale"
                else
                    warn "brew install tailscale failed"
                fi
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
            info "▸ Next: 'sudo tailscale up' — this may ask for your password, then opens a"
            info "  browser tab for sign-in. Sign in to the SAME tailnet as your Dejima server."
            info "  (Ctrl-C to skip; sign in later with 'sudo tailscale up'.)"
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

# probe_host: does something answer the daemon's port on this address?
probe_host() {
    if command -v nc >/dev/null 2>&1; then
        nc -z -w2 "$1" 7273 >/dev/null 2>&1
    else
        # Fallback: bash's /dev/tcp (works on macOS bash too)
        (exec 3<>/dev/tcp/"$1"/7273) 2>/dev/null || return 1
        exec 3<&- 2>/dev/null || true
        exec 3>&- 2>/dev/null || true
    fi
}

# Tailscale already knows every machine on this tailnet. Sending the user to a
# DIFFERENT computer to run `tailscale ip -4`, write the number down, and carry
# it back is asking them to perform a lookup we can do from here — and it was
# the step where the answer to "wait, what am I supposed to enter?" was not
# obvious. Ask the tailnet, and offer whatever is running a Dejima daemon.
discovered=""
if command -v tailscale >/dev/null 2>&1 && tailscale status >/dev/null 2>&1; then
    info "Looking for your Dejima server on your Tailscale network…"
    for ts_ip in $(tailscale status 2>/dev/null | awk '$1 ~ /^100\./ {print $1}'); do
        if probe_host "$ts_ip"; then
            discovered="$discovered $ts_ip"
        fi
    done
    discovered="${discovered# }"
fi

# Lead with the common case. This used to open with the invite escape hatch,
# which reads as a question you have to answer ("am I using an invite?") — and
# for the solo operator, who set the server up themselves and has no teammate to
# get a code from, the answer is always no. The invite line stays, demoted to
# what it is: the exception.
default_host=""
if [[ -n "$discovered" && "$discovered" != *" "* ]]; then
    default_host="$discovered"
    ok "Found a Dejima server at $default_host"
    info "Press Enter to use it, or type a different address."
elif [[ -n "$discovered" ]]; then
    info "Found more than one Dejima server on your network:"
    for ts_ip in $discovered; do info "  $ts_ip"; done
    info "Type the one you want below."
else
    info "TYPE THE ADDRESS of the Mac you installed Dejima on — its Tailscale IP,"
    info "which looks like 100.84.12.7. To find it, run this ON THAT MAC:"
    info "    tailscale ip -4"
    info "(If Tailscale isn't signed in on this Mac yet, press Enter to skip — you"
    info " can set the address later with: dejima profile add <name> <ip>:7273)"
fi
info "(Joining someone else's server from a 'dejima-invite:' code? Press Enter to"
info " skip this, then run 'dejima join <invite>' — it carries the address + token.)"

server_host=""
if [[ -e /dev/tty && -z "${DEJIMA_HOST_PREFILL:-}" ]]; then
    # Default port assumed below; user types IP or hostname only. The '▸' marks
    # this as an input prompt so it doesn't read as a blank/hung line.
    if [[ -n "$default_host" ]]; then
        read -r -p "▸ Server address [$default_host]: " server_host </dev/tty 2>/dev/null || true
        server_host="${server_host:-$default_host}"
    else
        read -r -p "▸ Type your server's Tailscale IP, e.g. 100.84.12.7 (or press Enter to skip): " server_host </dev/tty 2>/dev/null || true
    fi
elif [[ -n "${DEJIMA_HOST_PREFILL:-}" ]]; then
    server_host="$DEJIMA_HOST_PREFILL"
elif [[ -n "$default_host" ]]; then
    # Non-interactive, but the tailnet answered — take it rather than finishing
    # the install with no server configured at all.
    server_host="$default_host"
fi

if [[ -n "$server_host" ]]; then
    # Strip a user-supplied port, then re-attach the canonical 7273.
    server_host="${server_host%:*}"
    candidate_host="${server_host}:7273"
    # BRACES ARE LOAD-BEARING. `$candidate_host…` — a variable abutting a
    # multibyte character with no delimiter — is read by macOS's bash 3.2 as one
    # long undefined NAME, and `set -u` then kills the installer at the last
    # step with "candidate_host?: unbound variable". Bash 5 parses it correctly,
    # which is why it survived: it fails only on the platform most users run it
    # on, and only for the users who actually answered the prompt.
    info "Probing ${candidate_host}…"
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

    # Persist a DURABLE connection profile in client.json — this survives shell-rc
    # edits and client updates, unlike the bare `export DEJIMA_HOST` below which an
    # update wiping ~/.zshenv silently strips, locking the user out with no message.
    # The profile is the durable store; the rc export stays as a current-shell
    # convenience. `add` errors if it already exists (re-install) — ignore that and
    # still `switch` so the profile becomes active either way.
    "$BIN_DIR/dejima" profile add "$server_host" "$candidate_host" >/dev/null 2>&1 || true
    if "$BIN_DIR/dejima" profile switch "$server_host" >/dev/null 2>&1; then
        ok "saved a durable connection profile ($server_host → $candidate_host)"
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
