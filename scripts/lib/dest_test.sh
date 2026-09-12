#!/usr/bin/env bash
# Tests for scripts/lib/dest.sh — the installer's local/host/client decision.
#
# The bug this covers: scripts/setup.sh never asked. It ran one path, and that
# path's first step is Tailscale, because it was written for the always-on Mac
# mini. `dejima onboard` learned to ask after an operator who wanted a daemon on
# her own Mac landed in host provisioning; the installer did not, and the
# installer is the door most people come through — dejima.tech's own "Run it
# locally" instructions are `curl … | bash`, which lands in setup.sh.
#
# Run:  scripts/lib/dest_test.sh
#
# Two properties are load-bearing and pull in opposite directions:
#
#   * a person who answers Local (or just presses Enter) must not get the host
#     path — the whole point;
#   * a run with NOBODY THERE must still be host — every runbook, CI job and doc
#     that pipes this script expects that, and quietly making them local would
#     take remote access off machines whose owners never saw a prompt.
#
# The last case is a NEGATIVE CONTROL: it re-runs the decisive assertion against
# a stub that ignores the answer and always says host, and requires it to FAIL.
# Without it a green run proves only that the test executed, not that it can see
# the regression.

set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1
TTYLIB="scripts/lib/tty.sh"
LIB="scripts/lib/dest.sh"
PTYRUN="scripts/lib/ptyrun.py"

if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 is needed to allocate a pty" >&2
    exit 0
fi

PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# probe <name> [extra-line] — a script that sources both libs, optionally
# overrides something, and echoes the decision.
probe() {
    local name="$1" extra="${2:-}"
    # SC2016 is the point: the $(…) below must reach the generated script
    # literally and be evaluated there, not expanded while writing it.
    # shellcheck disable=SC2016
    printf '%s\n' \
        'set -uo pipefail' \
        ". $PWD/$TTYLIB" \
        ". $PWD/$LIB" \
        "$extra" \
        'echo "DEST=$(setup_destination)"' > "$TMP/$name.sh"
    printf '%s' "$TMP/$name.sh"
}

# check <description> <expected-substring> <mode> <script> [stdin-feed] [env...]
check() {
    local desc="$1" want="$2" mode="$3" script="$4" feed="${5:-}"
    shift 5 2>/dev/null || shift 4
    local got
    got="$(printf '%s' "$feed" | env "$@" python3 "$PTYRUN" "$mode" "$script" 2>&1)"
    if [[ "$got" == *"$want"* ]]; then
        printf '  \033[32m✓\033[0m %s\n' "$desc"
        PASS=$((PASS + 1))
    else
        printf '  \033[31m✗\033[0m %s\n' "$desc"
        printf '      want substring: %q\n' "$want"
        printf '      got:            %q\n' "$got"
        FAIL=$((FAIL + 1))
    fi
}

printf '\033[1mdest.sh\033[0m\n'

ASK="$(probe ask)"

# --- a person is there ------------------------------------------------------
# The #341 shape: stdin is the curl pipe, but someone is watching.
check "bare Enter is Local under curl | bash"      DEST=local  pipe "$ASK" $'\n'
check "l chooses Local under curl | bash"          DEST=local  pipe "$ASK" $'l\n'
check "h chooses Host under curl | bash"           DEST=host   pipe "$ASK" $'h\n'
check "c chooses Client under curl | bash"         DEST=client pipe "$ASK" $'c\n'
check "h chooses Host interactively"               DEST=host   tty  "$ASK" $'h\n'
# A typo must not provision a machine.
check "an unrecognised key falls to Local"         DEST=local  pipe "$ASK" $'x\n'

# --- nobody is there --------------------------------------------------------
# The compatibility guarantee. This is the case that must NOT become local.
check "headless stays on Host"                     DEST=host   none "$ASK"

# --- scripted callers -------------------------------------------------------
check "DEJIMA_SETUP=local is honoured headless"    DEST=local  none "$ASK" "" DEJIMA_SETUP=local
check "DEJIMA_SETUP=host is honoured"              DEST=host   none "$ASK" "" DEJIMA_SETUP=host
check "DEJIMA_SETUP=client is honoured"            DEST=client none "$ASK" "" DEJIMA_SETUP=client
# A typo'd value must stop, not silently mean host — the one outcome a caller
# spelling out its intent must never get by accident.
check "a bad DEJIMA_SETUP is rejected, not guessed" "must be local, host or client" \
                                                   none "$ASK" "" DEJIMA_SETUP=hsot
check "a bad DEJIMA_SETUP prints no decision"      "DEST="     none "$ASK" "" DEJIMA_SETUP=hsot

# --- negative control -------------------------------------------------------
# Against a stub that ignores the answer and always says host — the behaviour
# before this existed — the decisive assertion must FAIL. If it passes here the
# assertion is not reading the decision at all.
STUB="$(probe stub 'setup_destination() { printf host; }')"
got="$(printf '%s' $'\n' | python3 "$PTYRUN" pipe "$STUB" 2>&1)"
if [[ "$got" == *"DEST=local"* ]]; then
    printf '  \033[31m✗\033[0m NEGATIVE CONTROL: the old always-host behaviour passed the Local check\n'
    FAIL=$((FAIL + 1))
else
    printf '  \033[32m✓\033[0m negative control: always-host fails the Local check, as it must\n'
    PASS=$((PASS + 1))
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
