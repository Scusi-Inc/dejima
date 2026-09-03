#!/usr/bin/env bash
# Does `ssh -L` really accept locally and only then fail on the remote dial?
#
#   scripts/verify-ssh-forward-semantics.sh [ssh-target]     (default: localhost)
#
# WHY THIS EXISTS. `dejima agent open` now distinguishes "the tunnel is up" from
# "the gateway is up", because those are different states and only the second one
# means the browser has somewhere to go. The distinction rests on a claim about
# ssh:
#
#   with -L, ssh binds and ACCEPTS on the local port straight after auth, and
#   does not dial the remote side until a connection arrives — so when the
#   remote target is dead, a client sees connect-then-EOF rather than
#   connection-refused.
#
# That claim comes from ssh's documented behaviour, and the unit tests model it
# with a fixture rather than observing it. Every link in the chain is reasonable
# and not one of them is a measurement:
#
#   documented -L semantics  ->  a fixture that models them  ->  a test that passes
#
# This script is the missing measurement. It needs no dejima, no island and no
# container — just an ssh you can log into — so the premise can be checked
# independently of everything built on top of it.
#
# It is deliberately not a harness for `dejima agent open` itself. That still
# needs a human to run it against an island whose agent is mid-`npm install`;
# see docs/testing/test-coverage-matrix.md. This settles the one part that can
# be settled mechanically.
#
# FIRST RUN: 2026-08-20, OpenSSH 9.6 (Ubuntu 24.04, aarch64), against a
# throwaway sshd. Result CONFIRMED — ssh logged "channel 2: open failed:
# connect failed: Connection refused" while the local end had already accepted.
# Control run, same script pointed at a remote port that WAS serving, reported
# UNEXPECTED rather than CONFIRMED, so the script distinguishes the two cases
# instead of always agreeing with itself.

set -uo pipefail

TARGET="${1:-localhost}"

die() {
    echo "$1" >&2
    exit 2
}

command -v ssh >/dev/null 2>&1 || die "ssh not found"
command -v nc >/dev/null 2>&1 || command -v python3 >/dev/null 2>&1 ||
    die "need nc or python3 to dial the forward"

# A port nothing is listening on, on the far side. Chosen high and unlikely; the
# script verifies it really is closed before drawing any conclusion, because
# "the remote was alive after all" would produce a confident wrong answer.
DEAD_REMOTE_PORT=59417
LOCAL_PORT=0

free_port() {
    python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

# Dial LOCAL_PORT, send a byte, and report what came back:
#   refused  - nothing accepted the connection
#   eof      - accepted, then closed with no data   <- the state under test
#   data     - accepted and answered
probe() {
    python3 - "$1" <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.socket()
s.settimeout(5)
try:
    s.connect(("127.0.0.1", port))
except (ConnectionRefusedError, OSError):
    print("refused"); sys.exit(0)
try:
    s.sendall(b"GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")
    print("data" if s.recv(1) else "eof")
except (socket.timeout, ConnectionResetError, BrokenPipeError, OSError):
    print("eof")
PY
}

command -v python3 >/dev/null 2>&1 || die "python3 is required for the probe"

echo "== ssh -L forward semantics =="
echo "target:        $TARGET"
echo "remote port:   $DEAD_REMOTE_PORT (expected to be closed)"
echo

LOCAL_PORT="$(free_port)"
echo "forwarding 127.0.0.1:$LOCAL_PORT -> $TARGET:127.0.0.1:$DEAD_REMOTE_PORT"

ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes \
    -L "${LOCAL_PORT}:127.0.0.1:${DEAD_REMOTE_PORT}" "$TARGET" &
SSH_PID=$!
# shellcheck disable=SC2064  # expand SSH_PID now, not at trap time
trap "kill $SSH_PID 2>/dev/null" EXIT

# Wait for the local end to bind, which is the thing the claim says happens
# BEFORE any remote dial.
BOUND=no
for _ in $(seq 1 30); do
    if [[ "$(probe "$LOCAL_PORT")" != "refused" ]]; then
        BOUND=yes
        break
    fi
    if ! kill -0 "$SSH_PID" 2>/dev/null; then
        break
    fi
    sleep 0.5
done

if ! kill -0 "$SSH_PID" 2>/dev/null; then
    echo
    echo "INCONCLUSIVE: ssh exited before the forward came up."
    echo "  Usually this is auth: try 'ssh $TARGET' by hand first. On macOS,"
    echo "  'localhost' needs Remote Login enabled (System Settings > General > Sharing)."
    echo "  Nothing is proved or disproved by this run."
    exit 1
fi

if [[ "$BOUND" != "yes" ]]; then
    echo
    echo "UNEXPECTED: ssh is alive but the local port never accepted a connection."
    echo "  That CONTRADICTS the premise (accept happens before the remote dial)."
    echo "  Worth reporting — cmd/dejima/agent_open.go is built on the opposite."
    exit 1
fi

RESULT="$(probe "$LOCAL_PORT")"
echo
case "$RESULT" in
eof)
    echo "CONFIRMED: the local end accepted, then closed with no data."
    echo
    echo "  This is the state 'the tunnel is up' cannot distinguish from a working"
    echo "  gateway, and the state gatewayReady() exists to reject. The fixture in"
    echo "  cmd/dejima/agent_open_gateway_test.go models exactly this."
    exit 0
    ;;
data)
    echo "UNEXPECTED: something answered on the remote port $DEAD_REMOTE_PORT."
    echo "  The port was supposed to be closed, so this run says nothing about"
    echo "  the failure case. Re-run with a port you know is free on $TARGET."
    exit 1
    ;;
refused)
    echo "UNEXPECTED: the local port refused a connection while ssh was running."
    echo "  That CONTRADICTS the premise. Worth reporting."
    exit 1
    ;;
*)
    echo "UNEXPECTED probe result: $RESULT"
    exit 1
    ;;
esac
