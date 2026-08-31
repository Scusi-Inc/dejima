#!/usr/bin/env bash
# Tests for scripts/lib/transcript.sh.
#
# The point of the transcript is a failed install being REPORTABLE, so the
# assertion that matters most is that the LAST lines before an exit are in the
# file. tee writes through a process substitution the shell does not wait for, so
# "the log exists" and "the log is complete" are different claims and only the
# second one is useful.
set -uo pipefail

LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PASS=0; FAIL=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Each case runs in its own shell: start_transcript redirects the shell's own
# stdout, which cannot be undone in-process.
run_case() { HOME="$TMP/$1" bash -c "mkdir -p \"\$HOME\"; cd '$LIB_DIR'; . ./transcript.sh; $2"; }

echo "transcript.sh"

# 1. The log is created, and the visible output still reaches the caller.
out="$(run_case one 'start_transcript; echo VISIBLE_LINE')"
if [[ "$out" == *VISIBLE_LINE* ]]; then
    ok "output still reaches the operator (tee, not a silent redirect)"
else
    bad "the install went silent — the operator sees nothing: '$out'"
fi
log="$(find "$TMP/one" -name 'dejima-install-*.log' 2>/dev/null | head -1)"
if [[ -n "$log" ]]; then ok "a log file is created"; else bad "no log file was created"; fi

# 2. THE CASE THAT MATTERS: the last line before exit is in the file.
run_case two 'start_transcript; echo EARLY_LINE; echo FINAL_LINE_BEFORE_EXIT' >/dev/null
log2="$(find "$TMP/two" -name 'dejima-install-*.log' 2>/dev/null | head -1)"
if [[ -n "$log2" ]] && grep -q FINAL_LINE_BEFORE_EXIT "$log2"; then
    ok "the LAST line before exit is captured (a truncated log is the useless kind)"
else
    bad "the final line is missing — the tail is exactly what a failure report needs"
fi
if [[ -n "$log2" ]] && grep -q EARLY_LINE "$log2"; then
    ok "earlier output is captured"
else
    bad "earlier output is missing from the log"
fi

# 3. The header says which machine and when, because that is the first question.
if [[ -n "$log2" ]] && grep -q "^date:" "$log2" && grep -q "^host:" "$log2"; then
    ok "the log is self-describing (date + host)"
else
    bad "the log has no header — 'which machine, when' is unanswerable"
fi

# 4. Idempotent across the install.sh → make setup handoff. install.sh exports
#    the variable and execs; setup.sh must not wrap a second time.
#    Checking that preset.log stays empty proves nothing — it stays empty either
#    way, because the re-wrap would create a DIFFERENT file. The real assertion
#    is that NO second log appears.
out4="$(DEJIMA_INSTALL_LOG="$TMP/preset.log" run_case four 'start_transcript; echo AFTER')"
extra="$(find "$TMP/four" -name 'dejima-install-*.log' 2>/dev/null | wc -l | tr -d ' ')"
if [[ "$out4" == *AFTER* ]] && [[ "$extra" == "0" ]]; then
    ok "a transcript already in progress is left alone (no second log)"
else
    bad "start_transcript re-wrapped an inherited transcript: $extra extra log(s)"
fi

# 5. An unwritable HOME must not stop the install.
out5="$(HOME=/proc/nonexistent bash -c "cd '$LIB_DIR'; . ./transcript.sh; start_transcript; echo STILL_RAN" 2>/dev/null)"
if [[ "$out5" == *STILL_RAN* ]]; then
    ok "an unopenable log does not abort the install"
else
    bad "the install died because it could not open a log file"
fi

# 6. transcript_note names the path, and says nothing when there is no log.
note="$(DEJIMA_INSTALL_LOG=/tmp/x.log bash -c "cd '$LIB_DIR'; . ./transcript.sh; transcript_note")"
if [[ "$note" == *"/tmp/x.log"* ]]; then ok "the failure note names the log"; else bad "the note omits the path: '$note'"; fi
note2="$(bash -c "cd '$LIB_DIR'; unset DEJIMA_INSTALL_LOG; . ./transcript.sh; transcript_note")"
if [[ -z "$note2" ]]; then ok "no log, no note"; else bad "it advertised a log that does not exist: '$note2'"; fi

echo
if [[ "$FAIL" -eq 0 ]]; then echo "PASS — $PASS checks."; else echo "FAIL — $FAIL of $((PASS+FAIL))."; fi
[[ "$FAIL" -eq 0 ]]
