#!/usr/bin/env bash
# Where install-client.sh puts the binary, and whether it can ever update itself.
#
# A client installed into a ROOT-OWNED directory can never self-update: the
# updater renames a staged file into place, a rename needs write permission on
# the DIRECTORY, and the `sudo -n` fallback needs a NOPASSWD rule that only
# `dejima service install --system` writes — which a client machine never runs.
# That was the DEFAULT on a fresh Apple Silicon Mac, where /usr/local/bin does
# not exist until the installer sudo-creates it. Reported from a real
# client-only Mac on 2026-09-01: every update failed with
# "replace /usr/local/bin/dejima: rename ...".
#
# This drives the REAL installer, not a copy of its logic, with curl and
# tailscale shimmed. A test that re-implemented the chooser would agree with
# itself forever.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
PASS=0; FAIL=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

VER="v9.9.9"
case "$(uname -s)" in Darwin) OS=darwin ;; *) OS=linux ;; esac
case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; *) ARCH=arm64 ;; esac
ASSET="dejima_${VER}_${OS}_${ARCH}.tar.gz"

# --- a fake release, served by a fake curl ---------------------------------
FIX="$(mktemp -d)"; trap 'rm -rf "$FIX"' EXIT
mkdir -p "$FIX/pkg"
printf '#!/bin/sh\necho dejima %s\n' "$VER" > "$FIX/pkg/dejima"
chmod +x "$FIX/pkg/dejima"
tar -czf "$FIX/$ASSET" -C "$FIX/pkg" dejima
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$FIX" && sha256sum "$ASSET" > SHA256SUMS)
else
    (cd "$FIX" && shasum -a 256 "$ASSET" > SHA256SUMS)
fi

SHIM="$(mktemp -d)"; trap 'rm -rf "$FIX" "$SHIM"' EXIT
cat > "$SHIM/curl" <<SHIMEOF
#!/usr/bin/env bash
# Serve the fixture instead of GitHub. Understands only what the installer uses:
# a URL somewhere in the args and an -o destination.
out=""; url=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -o) out="\$2"; shift 2 ;;
    http*) url="\$1"; shift ;;
    *) shift ;;
  esac
done
[ -n "\$url" ] || exit 1
src="$FIX/\$(basename "\$url")"
[ -f "\$src" ] || exit 22
if [ -n "\$out" ]; then cp "\$src" "\$out"; else cat "\$src"; fi
SHIMEOF
# Present so the Tailscale section reports "found" and installs nothing.
printf '#!/bin/sh\nexit 0\n' > "$SHIM/tailscale"
chmod +x "$SHIM/curl" "$SHIM/tailscale"

# run_case <home> <extra env assignments...> — returns installer output
run_case() {
    local home="$1"; shift
    env -i \
        HOME="$home" \
        PATH="${EXTRA_PATH:+$EXTRA_PATH:}$SHIM:/usr/bin:/bin" \
        SHELL=/bin/zsh \
        DEJIMA_VERSION="$VER" \
        DEJIMA_HOST_PREFILL="skip" \
        "$@" \
        bash "$ROOT/install-client.sh" 2>&1
}

echo "install-client.sh: install location and self-updatability"

# --- 1. system dir NOT writable: must land somewhere the user owns ---------
H1="$(mktemp -d)"
SYS1="$(mktemp -d)/usr-local"; mkdir -p "$SYS1/bin"
# The stale binary a previous install left behind, in a directory that is on
# PATH and NOT writable — Amanda's Mac exactly.
printf '#!/bin/sh\necho dejima v0.0.1-stale\n' > "$SYS1/bin/dejima"
chmod +x "$SYS1/bin/dejima"; chmod 555 "$SYS1/bin"
out1="$(EXTRA_PATH="$SYS1/bin" run_case "$H1" DEJIMA_SYSTEM_PREFIX="$SYS1")"

if [[ -x "$H1/.local/bin/dejima" ]]; then
    ok "unwritable system dir → installed to ~/.local/bin"
else
    bad "unwritable system dir → nothing at ~/.local/bin:
$out1"
fi
# Distinguish "did not INSTALL there" from "the stale file is gone", which are
# different facts. An earlier version of this check conflated them and passed
# because remove_stale_binary had sudo-deleted the fixture — it would have gone
# green for a build that installed into the root-owned directory.
if [[ -e "$SYS1/bin/dejima" ]] && grep -q "$VER" "$SYS1/bin/dejima" 2>/dev/null; then
    bad "installed the NEW binary into the unwritable system dir (via sudo?)"
else
    ok "did not install into the root-owned system dir"
fi
# The whole point: the install must be updatable without sudo forever after.
if [[ -w "$H1/.local/bin" ]]; then
    ok "install dir is writable, so self-update can rename into place"
else
    bad "install dir is NOT writable — self-update will fail exactly as before"
fi
if grep -q 'added by the dejima installer' "$H1/.zshrc" 2>/dev/null; then
    ok "PATH line added to .zshrc"
else
    bad "no PATH line in .zshrc — the binary would not be found:
$(cat "$H1/.zshrc" 2>/dev/null)"
fi
# PREPENDED, not appended: a stale /usr/local/bin/dejima sits earlier in the
# default PATH, so an appended entry would install a binary that never runs.
# shellcheck disable=SC2016  # $PATH is matched LITERALLY: it must reach the rc file unexpanded
if grep -q 'export PATH="'"$H1"'/.local/bin:$PATH"' "$H1/.zshrc" 2>/dev/null; then
    ok "PATH entry is PREPENDED"
else
    bad "PATH entry is not prepended; a stale binary earlier on PATH would win:
$(grep -i path "$H1/.zshrc" 2>/dev/null)"
fi
if grep -qi "open a new terminal" <<<"$out1"; then
    ok "tells the user to open a new terminal"
else
    bad "never says to open a new terminal — the #1 'it did nothing' trap"
fi
# An older root-owned dejima stays visible to sudo, cron, and any shell that
# does not read the rc file. Silently leaving it is how someone updates, sees
# no change, and reports the update as broken.
if grep -q "older dejima is still installed" <<<"$out1"; then
    ok "surfaces the stale binary that would otherwise shadow the new one"
else
    bad "never mentions the stale binary at $SYS1/bin/dejima:
$out1"
fi

# --- 2. system dir writable: nothing changes for installs working today ----
H2="$(mktemp -d)"
SYS2="$(mktemp -d)/usr-local"; mkdir -p "$SYS2/bin"
out2="$(EXTRA_PATH="$SYS2/bin" run_case "$H2" DEJIMA_SYSTEM_PREFIX="$SYS2")"
if [[ -x "$SYS2/bin/dejima" ]]; then
    ok "writable system dir → still installs there (no change for today's users)"
else
    bad "writable system dir → did NOT install there:
$out2"
fi
if [[ -e "$H2/.local/bin/dejima" ]]; then
    bad "installed to ~/.local/bin even though the system dir was writable"
else
    ok "left ~/.local/bin alone"
fi
if grep -q 'added by the dejima installer' "$H2/.zshrc" 2>/dev/null; then
    bad "edited PATH despite installing to an already-on-PATH location"
else
    ok "no gratuitous PATH edit"
fi

# --- 3. explicit PREFIX is still honoured ----------------------------------
H3="$(mktemp -d)"; P3="$(mktemp -d)/opt"
out3="$(run_case "$H3" PREFIX="$P3")"
if [[ -x "$P3/bin/dejima" ]]; then
    ok "explicit PREFIX honoured"
else
    bad "explicit PREFIX ignored:
$out3"
fi

echo
if [[ "$FAIL" -eq 0 ]]; then
    printf '\033[1mPASS — %d checks.\033[0m\n' "$PASS"
else
    printf '\033[1mFAIL — %d of %d checks failed.\033[0m\n' "$FAIL" "$((PASS+FAIL))"
fi
[[ "$FAIL" -eq 0 ]]
