#!/usr/bin/env bash
# Tests for scripts/lib/dockerwait.sh — what the installer says while it waits
# for Docker Desktop's first launch.
#
# The bug this covers: on a Mac mini install, `open -a Docker` returned success
# without bringing the app up. The wait loop then printed "CHECK THIS MAC'S
# SCREEN: first launch shows a licence agreement that must be accepted" every
# 30 seconds, at a screen with nothing on it, for five minutes. The operator
# eventually clicked Docker by hand and it worked immediately. The loop had the
# information to tell her that — the app was not running — and never used it.
#
# The same run printed
#   scripts/setup.sh: line 334: 34601 Killed: 9   docker version > /dev/null 2>&1
# because macOS SIGKILLs the CLI while Docker Desktop replaces it during first
# launch. Harmless, and it reads like the installer itself was killed.
#
# Run:  scripts/lib/dockerwait_test.sh
#
# The last case is a NEGATIVE CONTROL: it runs the decisive assertion against
# the OLD probe and requires it to FAIL. Without it a green run proves only
# that the test executed, not that it can still see the regression.

set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1
LIB="scripts/lib/dockerwait.sh"

PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# shellcheck source=scripts/lib/dockerwait.sh
. "$LIB"

# want <description> <expected-substring> <output>
want() {
    local desc="$1" needle="$2" got="$3"
    if [[ "$got" == *"$needle"* ]]; then
        printf '  \033[32m✓\033[0m %s\n' "$desc"
        PASS=$((PASS + 1))
    else
        printf '  \033[31m✗\033[0m %s\n' "$desc"
        printf '      want substring: %q\n' "$needle"
        printf '      got:            %q\n' "$got"
        FAIL=$((FAIL + 1))
    fi
}

# reject <description> <forbidden-substring> <output>
reject() {
    local desc="$1" needle="$2" got="$3"
    if [[ "$got" != *"$needle"* ]]; then
        printf '  \033[32m✓\033[0m %s\n' "$desc"
        PASS=$((PASS + 1))
    else
        printf '  \033[31m✗\033[0m %s\n' "$desc"
        printf '      forbidden substring present: %q\n' "$needle"
        printf '      got: %q\n' "$got"
        FAIL=$((FAIL + 1))
    fi
}

printf '\033[1mdockerwait.sh — advice\033[0m\n'

# THE CASE THAT HAPPENED. Console user is the operator, app never came up:
# the only useful instruction is "click it yourself", and the old text said the
# opposite — that something was on screen waiting to be accepted.
got="$(docker_wait_advice 0 amanda amanda)"
want "not running: says so" "is NOT running" "$got"
want "not running: says to open it by hand" "click Docker in /Applications" "$got"
reject "not running: does not claim a prompt is on screen" "waiting for you ON THIS MAC'S SCREEN" "$got"

# Running, and someone is at the display: now the licence text is the right
# thing to say, and it is the ONLY case where it is.
got="$(docker_wait_advice 1 amanda amanda)"
want "running: says it is up" "IS running" "$got"
want "running: names the licence agreement" "licence agreement" "$got"
reject "running: does not tell them to launch it again" "click Docker in /Applications" "$got"

# Nobody at the display (login window, or a headless mac mini). Waiting cannot
# work here no matter which of the two states the app is in, and that is worth
# saying out loud rather than counting to 300.
got="$(docker_wait_advice 1 "" amanda)"
want "no console session: says nobody is logged in" "nobody is logged in" "$got"
want "no console session: says waiting will not help" "waiting will not clear this" "$got"
want "no console session: names the account to log in as" "as amanda" "$got"

got="$(docker_wait_advice 0 "" amanda)"
want "no console session, app down: still says nobody is logged in" "nobody is logged in" "$got"

# A different account owns the display: the prompt appears in THEIR session.
got="$(docker_wait_advice 1 sam amanda)"
want "other console user: names who holds the display" "logged in as sam" "$got"
want "other console user: names who is installing" "running as amanda" "$got"

printf '\033[1mdockerwait.sh — probe\033[0m\n'

# A `docker` that dies the way Docker Desktop's does mid-swap: SIGKILL, no
# output of its own. The notice is printed by the CALLING shell, so the
# command's own redirect cannot suppress it — which is why the probe wraps the
# whole thing.
cat > "$TMP/docker" <<'EOF'
#!/bin/sh
kill -9 $$
EOF
chmod +x "$TMP/docker"

probe_out="$(PATH="$TMP:$PATH" bash -c '
    set -uo pipefail
    . scripts/lib/dockerwait.sh
    if docker_cli_ok; then echo "REPORTED-UP"; else echo "reported-down"; fi
' 2>&1)"
want "killed CLI reads as down" "reported-down" "$probe_out"
reject "killed CLI does not print a kill notice" "Killed" "$probe_out"

# Ordinary outcomes still work: a CLI that fails is down, one that succeeds is up.
cat > "$TMP/docker" <<'EOF'
#!/bin/sh
echo "Cannot connect to the Docker daemon" >&2
exit 1
EOF
chmod +x "$TMP/docker"
probe_out="$(PATH="$TMP:$PATH" bash -c '. scripts/lib/dockerwait.sh; docker_cli_ok && echo UP || echo down' 2>&1)"
want "failing CLI reads as down" "down" "$probe_out"
reject "failing CLI output stays quiet" "Cannot connect" "$probe_out"

cat > "$TMP/docker" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$TMP/docker"
probe_out="$(PATH="$TMP:$PATH" bash -c '. scripts/lib/dockerwait.sh; docker_cli_ok && echo UP || echo down' 2>&1)"
want "working CLI reads as up" "UP" "$probe_out"

printf '\033[1mdockerwait.sh — the wait loop\033[0m\n'

# run_loop <max-seconds> <docker-script> <pgrep-rc> — run the real loop with
# the world stubbed out: no sleeping, no launching, output captured. The stubs
# are the seams the lib was split out for; the loop itself is the real one.
run_loop() {
    local max="$1" docker_body="$2" pgrep_rc="$3"
    printf '%s\n' "$docker_body" > "$TMP/docker"
    chmod +x "$TMP/docker"
    printf '#!/bin/sh\nexit %s\n' "$pgrep_rc" > "$TMP/pgrep"
    chmod +x "$TMP/pgrep"
    PATH="$TMP:$PATH" COUNT="$TMP/count" bash -c '
        set -uo pipefail
        . scripts/lib/dockerwait.sh
        info() { printf "INFO %s\n" "$*"; }
        warn() { printf "WARN %s\n" "$*"; }
        sleep() { :; }                       # no real waiting in a test
        docker_relaunch() { echo "RELAUNCHED"; }
        console_user() { echo amanda; }      # no /dev/console in a container
        whoami() { echo amanda; }
        if docker_wait_for_daemon '"$max"'; then echo "RC=0"; else echo "RC=1"; fi
    ' 2>&1
}

DOCKER_DOWN='#!/bin/sh
exit 1'

# THE RUN THAT HAPPENED: the daemon never comes up and no Docker Desktop
# process ever exists. The loop must call that out early instead of narrating
# a licence prompt nobody can see.
got="$(run_loop 40 "$DOCKER_DOWN" 1)"
want "app never started: says so, early" "WARN Docker Desktop has not started" "$got"
want "app never started: tries the launch once more" "RELAUNCHED" "$got"
want "app never started: says to click it" "click Docker in /Applications" "$got"
reject "app never started: never claims a prompt is on screen" "waiting for you ON THIS MAC" "$got"
want "app never started: times out rather than hanging" "RC=1" "$got"
# The 15s call-out has to beat the 30s nudge — the whole point is not making
# the operator wait for the next heartbeat to learn the launch failed.
first_notice="$(printf '%s\n' "$got" | grep -n -m1 'has not started' | cut -d: -f1)"
first_nudge="$(printf '%s\n' "$got" | grep -n -m1 'still waiting' | cut -d: -f1)"
if [[ -n "$first_notice" && -n "$first_nudge" && "$first_notice" -lt "$first_nudge" ]]; then
    printf '  \033[32m✓\033[0m %s\n' "app never started: the call-out comes before the first 30s nudge"
    PASS=$((PASS + 1))
else
    printf '  \033[31m✗\033[0m %s (notice=%s nudge=%s)\n' \
        "app never started: the call-out comes before the first 30s nudge" "$first_notice" "$first_nudge"
    FAIL=$((FAIL + 1))
fi

# Docker Desktop IS up and the daemon still isn't: now — and only now — the
# licence agreement is the right thing to talk about.
got="$(run_loop 35 "$DOCKER_DOWN" 0)"
want "app running: says the prompt is on screen" "waiting for you ON THIS MAC" "$got"
want "app running: names the licence agreement" "licence agreement" "$got"
reject "app running: does not claim the launch failed" "has not started" "$got"
reject "app running: does not relaunch what is already up" "RELAUNCHED" "$got"

# The ordinary success: the daemon answers on the third probe. No advice, no
# nudges, no five-minute wait.
#
# The single quotes are load-bearing: this is the BODY of a fake `docker`, and
# $COUNT and $n have to reach it unexpanded to be evaluated when it runs.
# shellcheck disable=SC2016
got="$(run_loop 40 '#!/bin/sh
n=$(cat "$COUNT" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$COUNT"
[ "$n" -ge 3 ]' 1)"
want "daemon comes up: returns success" "RC=0" "$got"
reject "daemon comes up: says nothing about licences" "licence" "$got"
reject "daemon comes up: no waiting nudge" "still waiting" "$got"
rm -f "$TMP/count"

printf '\033[1mnegative control\033[0m\n'

# The old probe, verbatim, against the same self-killing CLI. If this does NOT
# leak the kill notice, the test above is not proving anything on this platform
# and the suite should say so rather than pass.
cat > "$TMP/docker" <<'EOF'
#!/bin/sh
kill -9 $$
EOF
chmod +x "$TMP/docker"
old_out="$(PATH="$TMP:$PATH" bash -c '
    set -uo pipefail
    if docker version >/dev/null 2>&1; then echo up; else echo down; fi
' 2>&1)"
want "the old probe DOES leak (proves this test can see it)" "Killed" "$old_out"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" == "0" ]]
