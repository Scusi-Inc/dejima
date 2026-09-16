#!/usr/bin/env bash
# The installer's SERVER-vs-CLIENT fork.
#
# install.sh builds the server stack: Go, Docker, an image, a daemon. Someone who
# only wanted to drive a server elsewhere used to be walked through all of it,
# with no signpost that install-client.sh exists — so every failure they could
# hit was in work they never needed to do. A fresh-Mac install failed three times
# in the field that way.
#
# Driven under a real pty, because the question is asked on /dev/tty: `curl … |
# bash` makes stdin a pipe while a person watches from the keyboard, and reading
# that as "nobody is here" is #341 itself.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
PASS=0; FAIL=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 needed for pty allocation — skipping"; exit 0
fi

echo "install.sh server/client fork"

# Answer "2" on the TTY and capture what the installer says. It must stop before
# doing any work and name the client script.
# `pipe` is the #341 shape: stdin is a pipe, the controlling terminal is real.
# That is `curl … | bash` with a person watching, which is how this is actually
# run — and the mode that proves the question reaches them at all.
out="$(printf '2\n' | timeout 60 python3 "$ROOT/scripts/lib/ptyrun.py" pipe "$ROOT/install.sh" 2>&1 || true)"

if grep -q "install-client.sh" <<<"$out"; then
    ok "choosing CLIENT names the client installer"
else
    bad "choosing CLIENT never mentions install-client.sh:
$out"
fi
if grep -q "install-client.ps1" <<<"$out"; then
    ok "and names the Windows one too"
else
    bad "no PowerShell path offered for a Windows client"
fi
# The whole point: it must NOT have started the server build.
# The server path prints these two lines immediately after the fork. Asserting
# on a LATER step would pass for the wrong reason here, where the run dies at
# "Go is required" before reaching a clone — a green that depends on the box
# being broken in a particular way.
if grep -qE "binaries will be installed to|source: " <<<"$out"; then
    bad "it began the SERVER install after being told this is a client:
$out"
else
    ok "it stops before any server work"
fi

# A piped, non-interactive run must fall through to the server path rather than
# hanging on a question nobody can answer — the unattended contract.
out2="$(printf '' | timeout 20 bash "$ROOT/install.sh" 2>&1 | head -20 || true)"
if grep -q "Choice \[L/h/c\]" <<<"$out2"; then
    bad "a non-interactive run asked the question anyway:
$out2"
elif ! grep -q "install type: host" <<<"$out2"; then
    # The control. Without it this passes when the script never ran: the absence
    # of a prompt is satisfied perfectly by producing no output, which is how the
    # PREVIOUS version of this check survived the prompt text changing under it.
    bad "a non-interactive run did not land on host:
$out2"
else
    ok "a non-interactive run does not ask, and lands on host"
fi

# DEJIMA_ROLE must let automation skip the prompt entirely.
# Read the TRANSCRIPT rather than the captured stream. install.sh redirects its
# own stdout through `tee` into the log, and a fast exit can tear that pipeline
# down before the capture sees anything — so asserting on $(...) here is a race
# that fails as "produced no output", which is indistinguishable from "never
# ran". The log is the durable record, and checking it exercises the transcript
# at the same time.
TMPH="$(mktemp -d)"
printf '' | HOME="$TMPH" DEJIMA_ROLE=server timeout 60 python3 "$ROOT/scripts/lib/ptyrun.py" pipe "$ROOT/install.sh" >/dev/null 2>&1 || true
out3="$(cat "$TMPH"/dejima-install-*.log 2>/dev/null || true)"
rm -rf "$TMPH"
if grep -q "Choice \[L/h/c\]" <<<"$out3"; then
    bad "DEJIMA_ROLE=server still prompted:
$out3"
elif ! grep -q "binaries will be installed to" <<<"$out3"; then
    # Without this, the check passes when the script never ran at all — the
    # absence of a prompt is satisfied perfectly by producing no output.
    bad "DEJIMA_ROLE=server did not reach the server path:
$out3"
else
    ok "DEJIMA_ROLE skips the prompt and proceeds"
fi

# --- LOCAL, the third answer -----------------------------------------------
# It existed in scripts/setup.sh and in `dejima onboard`, and not here — so the
# door most people come through offered two of the three. Worse, the two scripts
# asked DIFFERENT questions at different moments: this one SERVER/CLIENT, then
# setup.sh Local/Host/Client ten minutes later, after the toolchain.

# Bare Enter is Local. An operator who did not read the menu gets the least
# destructive answer, not a machine provisioned to stay awake.
TMPL="$(mktemp -d)"
printf '\n' | HOME="$TMPL" timeout 60 python3 "$ROOT/scripts/lib/ptyrun.py" pipe "$ROOT/install.sh" >/dev/null 2>&1 || true
outL="$(cat "$TMPL"/dejima-install-*.log 2>/dev/null || true)"
rm -rf "$TMPL"
if grep -q "install type: local" <<<"$outL"; then
    ok "bare Enter selects Local"
else
    bad "bare Enter did not select Local:
$outL"
fi

# And Local must still BUILD — it is the same server stack as host, minus the
# tailnet. A Local answer that bounced to the client script (or stopped) would
# leave the operator with no daemon at all.
if grep -q "binaries will be installed to" <<<"$outL"; then
    ok "Local proceeds with the build"
else
    bad "Local did not reach the build:
$outL"
fi

# DEJIMA_SETUP is what dejima.tech documents against this URL, and what
# scripts/setup.sh reads. This script ignored it entirely.
TMPS="$(mktemp -d)"
printf '' | HOME="$TMPS" DEJIMA_SETUP=local timeout 60 python3 "$ROOT/scripts/lib/ptyrun.py" pipe "$ROOT/install.sh" >/dev/null 2>&1 || true
outS="$(cat "$TMPS"/dejima-install-*.log 2>/dev/null || true)"
rm -rf "$TMPS"
if grep -q "Choice \[L/h/c\]" <<<"$outS"; then
    bad "DEJIMA_SETUP=local still prompted:
$outS"
elif ! grep -q "install type: local" <<<"$outS"; then
    bad "DEJIMA_SETUP=local did not select Local:
$outS"
else
    ok "DEJIMA_SETUP=local skips the prompt and selects Local"
fi

# A typo must stop the run. Falling through to host is the one outcome a caller
# spelling out its intent must never get by accident — dest.sh's rule, and the
# reason it is duplicated here rather than assumed.
TMPB="$(mktemp -d)"
outB="$(printf '' | HOME="$TMPB" DEJIMA_SETUP=lcoal timeout 20 bash "$ROOT/install.sh" 2>&1 | head -20 || true)"
rm -rf "$TMPB"
if grep -q "install type:" <<<"$outB"; then
    bad "a typo'd DEJIMA_SETUP proceeded anyway:
$outB"
elif ! grep -q "DEJIMA_SETUP must be" <<<"$outB"; then
    bad "a typo'd DEJIMA_SETUP gave no usable error:
$outB"
else
    ok "a typo'd DEJIMA_SETUP stops the run and says why"
fi

# A machine that was a CLIENT keeps DEJIMA_HOST in its shell rc. Install local
# on top and the CLI keeps talking to the old server — daemon up, `dejima ls`
# showing somebody else's fleet, nothing saying why.
TMPR="$(mktemp -d)"
printf 'export DEJIMA_HOST=old-server:7273\n' > "$TMPR/.zshrc"
printf '\n' | HOME="$TMPR" timeout 60 python3 "$ROOT/scripts/lib/ptyrun.py" pipe "$ROOT/install.sh" >/dev/null 2>&1 || true
outR="$(cat "$TMPR"/dejima-install-*.log 2>/dev/null || true)"
rm -rf "$TMPR"
if grep -q "already pointed at a Dejima server" <<<"$outR"; then
    ok "a stale DEJIMA_HOST from a prior client install is named"
else
    bad "a local install over a client config said nothing about DEJIMA_HOST:
$outR"
fi

echo
if [[ "$FAIL" -eq 0 ]]; then echo "PASS — $PASS checks."; else echo "FAIL — $FAIL of $((PASS+FAIL))."; fi
[[ "$FAIL" -eq 0 ]]
