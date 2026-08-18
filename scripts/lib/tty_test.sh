#!/usr/bin/env bash
# Tests for scripts/lib/tty.sh — the installer's "is anyone home?" decision.
#
# The bug this covers (#341): `curl -fsSL https://dejima.tech/install.sh | bash`
# leaves stdin as the pipe, so `[[ -t 0 ]]` reports "non-interactive" while a
# person watches from the keyboard. The installer took that to mean nobody was
# there — it installed Docker and Tailscale without asking, and skipped sudo
# pre-authorization, so Homebrew's own sudo prompt landed mid-cask-install and
# took the operator's password with terminal echo still on.
#
# Run:  scripts/lib/tty_test.sh
#
# The last case is a NEGATIVE CONTROL: it re-runs the decisive assertion against
# the old `[[ -t 0 ]]` logic and requires it to FAIL. Without it a green run
# here proves only that the test executed, not that it can see the regression —
# the failure mode this repo has been bitten by more than once.

set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1
LIB="scripts/lib/tty.sh"
PTYRUN="scripts/lib/ptyrun.py"

if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 is needed to allocate a pty" >&2
    exit 0
fi

PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# probe <name> <body> — write a script that sources the lib and runs <body>.
probe() {
    printf '%s\n' \
        'set -uo pipefail' \
        ". $PWD/$LIB" \
        "$2" > "$TMP/$1.sh"
    printf '%s' "$TMP/$1.sh"
}

# check <description> <expected-substring> <mode> <script> [stdin-feed]
check() {
    local desc="$1" want="$2" mode="$3" script="$4" feed="${5:-}"
    local got
    got="$(printf '%s' "$feed" | python3 "$PTYRUN" "$mode" "$script" 2>&1)"
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

printf '\033[1mtty.sh\033[0m\n'

# --- have_tty ---------------------------------------------------------------
DETECT="$(probe detect 'have_tty && echo HUMAN || echo HEADLESS')"

check "interactive run finds the terminal"        HUMAN    tty  "$DETECT"
# The regression. `[[ -t 0 ]]` says false here; /dev/tty says a human is present.
check "curl | bash still finds the terminal"      HUMAN    pipe "$DETECT"
check "no controlling terminal reports headless"  HEADLESS none "$DETECT"

# --- prompt_yn --------------------------------------------------------------
# The consequence that reached the operator: an unasked "yes" that installed
# Docker Desktop on a machine whose owner was never given the choice.
ASK="$(probe ask 'if prompt_yn "Install Docker Desktop now?" "y"; then echo ANSWER=yes; else echo ANSWER=no; fi')"

check "a declined prompt is honored under curl | bash" ANSWER=no  pipe "$ASK" $'n\n'
check "an accepted prompt is honored under curl | bash" ANSWER=yes pipe "$ASK" $'y\n'
check "bare Enter takes the default"                    ANSWER=yes pipe "$ASK" $'\n'
check "a declined prompt is honored interactively"      ANSWER=no  tty  "$ASK" $'n\n'
# Genuinely headless: must take the default rather than block forever waiting
# for an answer that cannot arrive.
check "headless takes the default without blocking"     ANSWER=yes none "$ASK"

AUTO="$(probe auto 'AUTO_INSTALL_DOCKER=1; if prompt_yn "x" "y"; then echo ANSWER=yes; else echo ANSWER=no; fi')"
check "AUTO_INSTALL_DOCKER=1 skips the prompt"          ANSWER=yes pipe "$AUTO"

# --- prime_sudo -------------------------------------------------------------
# Not that it obtains a ticket (that needs a password), but that it takes the
# right branch: under curl | bash it must NOT decline to prompt.
PRIME="$(probe prime 'have_tty && echo WOULD_PRIME || echo WOULD_SKIP')"
check "sudo priming is attempted under curl | bash"     WOULD_PRIME pipe "$PRIME"
check "sudo priming is skipped when headless"           WOULD_SKIP  none "$PRIME"

# A headless prime_sudo must return promptly and not wedge the run.
NOHANG="$(probe nohang 'prime_sudo "test" && echo RETURNED')"
check "prime_sudo returns when there is no terminal"    RETURNED    none "$NOHANG"

# --- negative control -------------------------------------------------------
# Re-run the decisive assertion against the logic this change replaced. If the
# old test also passes, this file is not measuring anything.
printf '\033[1mnegative control\033[0m\n'
cat > "$TMP/old.sh" <<'EOF'
set -uo pipefail
# The pre-#341 check, verbatim.
if [[ -t 0 ]]; then echo HUMAN; else echo HEADLESS; fi
EOF
old_out="$(python3 "$PTYRUN" pipe "$TMP/old.sh" </dev/null 2>&1)"
if [[ "$old_out" == *HEADLESS* ]]; then
    printf '  \033[32m✓\033[0m old [[ -t 0 ]] logic fails this case, as it must\n'
    PASS=$((PASS + 1))
else
    printf '  \033[31m✗\033[0m old logic PASSED under curl | bash — the test cannot see the bug\n'
    printf '      got: %q\n' "$old_out"
    FAIL=$((FAIL + 1))
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
