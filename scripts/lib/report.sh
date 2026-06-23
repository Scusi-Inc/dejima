# shellcheck shell=bash
# Structured per-feature reporting for the live test suites.
#
# Sourced by scripts/integration.sh (Tier-2) — and usable by the tier3/tier4
# suites — to turn a flat stream of pass/fail lines into a machine-readable
# per-feature summary. The design goal (Lane 6 Phase C): a single dispatch
# exercises every feature once, and the orchestrator can read a JSON artifact
# (pass/fail per feature) instead of scraping logs, and open/update a GitHub
# issue from the failing detail.
#
# Contract for the sourcing script:
#   - call `feature "<name>"` at the top of each feature block (this also acts
#     as the `step` banner — it prints the same bold header);
#   - keep using `pass`/`fail` exactly as before (these versions tally per the
#     CURRENT feature as well as globally);
#   - call `report_summary` at the end to print the rollup and, when the env var
#     DEJIMA_REPORT is set to a path, write the JSON summary there.
#
# The helpers are intentionally dependency-free (pure bash + printf): the runner
# has no guaranteed jq, so the JSON is emitted by hand with proper escaping.

# Global tallies (the sourcing script may already declare PASS/FAIL=0; keep them).
PASS=${PASS:-0}
FAIL=${FAIL:-0}

# Per-feature state. Bash 3.2 on macOS has no associative arrays we can rely on,
# so feature results are accumulated into a newline-delimited record string:
#   <status>\t<feature>\t<pass_count>\t<fail_count>\t<first_failure_detail>
_REPORT_RECORDS=""
_CUR_FEATURE=""
_CUR_PASS=0
_CUR_FAIL=0
_CUR_FIRST_FAIL=""

# _flush_feature records the current feature's tallies (if any) into the record
# string, then resets the per-feature counters.
_flush_feature() {
  [ -n "$_CUR_FEATURE" ] || return 0
  local status="pass"
  [ "$_CUR_FAIL" -eq 0 ] || status="fail"
  # Tab-separated; the detail is the LAST field so embedded spaces are fine.
  _REPORT_RECORDS="${_REPORT_RECORDS}${status}	${_CUR_FEATURE}	${_CUR_PASS}	${_CUR_FAIL}	${_CUR_FIRST_FAIL}
"
  _CUR_FEATURE="" _CUR_PASS=0 _CUR_FAIL=0 _CUR_FIRST_FAIL=""
}

# feature <name>: open a new feature block. Prints the same bold banner `step`
# would, and starts a fresh per-feature tally.
feature() {
  _flush_feature
  _CUR_FEATURE="$1"
  printf '\n\033[1m• %s\033[0m\n' "$1"
}

# pass/fail: tally globally AND against the current feature. These intentionally
# shadow the plain versions in the sourcing script (source report.sh AFTER them).
pass() {
  printf '  \033[32m✓\033[0m %s\n' "$*"
  PASS=$((PASS + 1))
  _CUR_PASS=$((_CUR_PASS + 1))
}
fail() {
  printf '  \033[31m✗\033[0m %s\n' "$*"
  FAIL=$((FAIL + 1))
  _CUR_FAIL=$((_CUR_FAIL + 1))
  [ -n "$_CUR_FIRST_FAIL" ] || _CUR_FIRST_FAIL="$*"
}

# _json_escape <string>: escape a string for embedding in a JSON value.
_json_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//	/\\t}
  # strip CRs and turn newlines into spaces (the detail is a single JSON string).
  s=${s//$'\r'/}
  s=${s//$'\n'/ }
  printf '%s' "$s"
}

# report_summary: print the human rollup, and — when DEJIMA_REPORT is set —
# write a JSON summary {suite, passed, failed, features:[{name,status,passed,
# failed,detail}]} to that path. Returns non-zero if any feature failed.
report_summary() {
  _flush_feature
  local suite="${DEJIMA_SUITE:-tier2-integration}"

  printf '\n\033[1m──────── per-feature results ────────\033[0m\n'
  local status feat fp ff detail
  while IFS=$'\t' read -r status feat fp ff detail; do
    [ -n "$feat" ] || continue
    if [ "$status" = "pass" ]; then
      printf '  \033[32m✓\033[0m %-34s %d passed\n' "$feat" "$fp"
    else
      printf '  \033[31m✗\033[0m %-34s %d passed, %d failed — %s\n' "$feat" "$fp" "$ff" "$detail"
    fi
  done <<EOF
$_REPORT_RECORDS
EOF

  printf '\n\033[1m──────── results ────────\033[0m\n'
  printf 'passed: %d   failed: %d\n' "$PASS" "$FAIL"

  if [ -n "${DEJIMA_REPORT:-}" ]; then
    _write_json "$DEJIMA_REPORT" "$suite"
    printf 'structured summary → %s\n' "$DEJIMA_REPORT"
  fi

  [ "$FAIL" -eq 0 ]
}

# _write_json <path> <suite>: emit the JSON summary by hand (no jq dependency).
_write_json() {
  local path=$1 suite=$2
  {
    printf '{\n'
    printf '  "suite": "%s",\n' "$(_json_escape "$suite")"
    printf '  "passed": %d,\n' "$PASS"
    printf '  "failed": %d,\n' "$FAIL"
    printf '  "features": [\n'
    local first=1 status feat fp ff detail
    while IFS=$'\t' read -r status feat fp ff detail; do
      [ -n "$feat" ] || continue
      [ "$first" -eq 1 ] || printf ',\n'
      first=0
      printf '    {"name": "%s", "status": "%s", "passed": %d, "failed": %d, "detail": "%s"}' \
        "$(_json_escape "$feat")" "$status" "$fp" "$ff" "$(_json_escape "$detail")"
    done <<EOF
$_REPORT_RECORDS
EOF
    printf '\n  ]\n}\n'
  } >"$path"
}
