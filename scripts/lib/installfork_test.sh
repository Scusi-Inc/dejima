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
if grep -q "Which is this?" <<<"$out2"; then
    bad "a non-interactive run asked the question anyway:
$out2"
else
    ok "a non-interactive run does not ask"
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
if grep -q "Which is this?" <<<"$out3"; then
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

echo
if [[ "$FAIL" -eq 0 ]]; then echo "PASS — $PASS checks."; else echo "FAIL — $FAIL of $((PASS+FAIL))."; fi
[[ "$FAIL" -eq 0 ]]
