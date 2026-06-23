#!/usr/bin/env bash
# Open or update a GitHub issue from a structured test-run summary on FAILURE.
#
# Reads the JSON summary written by scripts/lib/report.sh (the {suite, passed,
# failed, features:[…]} shape), and — only when failed > 0 — files a tracking
# issue with the per-feature failing detail, or updates the open one for the
# same suite (deduped by a stable title) with a fresh comment. On a clean run it
# CLOSES any open issue for that suite, so the tracker reflects current state.
#
# Designed to run as a CI step after the live suite:
#   DEJIMA_REPORT=report.json scripts/integration.sh || true
#   scripts/report-to-issue.sh report.json
# It is a no-op (exit 0) if the report is missing/clean, so a partially
# provisioned runner never turns the step red on its own.
#
# Requires: gh (authenticated; GH_TOKEN/GITHUB_TOKEN in CI), python3 (for robust
# JSON parsing — already a runner dependency for the parity check).
#
# PORTABILITY: the self-hosted macOS runner ships bash 3.2, whose parser scans
# for matching quotes *inside* `$( … )` command substitution even within a
# heredoc body — so `BODY="$(cat <<EOF … the run's … EOF)"` blows up with
# "unexpected EOF while looking for matching '" on the apostrophe. We therefore
# build the issue body in a temp file with a plain (non-substitution) heredoc and
# printf, then pass it via `--body-file` — safe on bash 3.2.

set -uo pipefail

REPORT="${1:-report.json}"
RUN_URL="${RUN_URL:-${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-}/actions/runs/${GITHUB_RUN_ID:-}}"
LABEL="${ISSUE_LABEL:-test-failure}"

# Temp files for the (bash-3.2-safe) body assembly; cleaned on exit. Inlined
# rather than a function so shellcheck doesn't flag the handler as unreachable
# (SC2317) — they're always set ("" until mktemp), so `rm -f` is a safe no-op.
DETAIL_FILE=""
BODY_FILE=""
trap 'rm -f "$DETAIL_FILE" "$BODY_FILE"' EXIT

if [ ! -f "$REPORT" ]; then
  echo "::notice::no report at $REPORT — skipping issue filing"
  exit 0
fi
if ! command -v gh >/dev/null 2>&1; then
  echo "::warning::gh not available — cannot file/update the test-failure issue"
  exit 0
fi

# Parse the summary into shell-friendly fields with python3 (no jq on the mini).
SUITE="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("suite","suite"))' "$REPORT")"
FAILED="$(python3 -c 'import json,sys;print(int(json.load(open(sys.argv[1])).get("failed",0)))' "$REPORT")"
PASSED="$(python3 -c 'import json,sys;print(int(json.load(open(sys.argv[1])).get("passed",0)))' "$REPORT")"

TITLE="Test failure: ${SUITE}"

# Find an existing open issue with this exact title (stable dedupe key).
EXISTING="$(gh issue list --state open --label "$LABEL" --search "in:title \"$TITLE\"" \
  --json number,title --jq ".[] | select(.title == \"$TITLE\") | .number" 2>/dev/null | head -1)"

if [ "$FAILED" -eq 0 ]; then
  # Clean run: close any open issue for this suite so the tracker is current.
  if [ -n "$EXISTING" ]; then
    gh issue comment "$EXISTING" --body "✅ ${SUITE} is green again ($PASSED passed) — ${RUN_URL}" >/dev/null 2>&1 || true
    gh issue close "$EXISTING" >/dev/null 2>&1 || true
    echo "closed issue #$EXISTING (suite green again)"
  else
    echo "::notice::${SUITE} clean ($PASSED passed) — no issue to file"
  fi
  exit 0
fi

# Build the failing-feature detail (markdown table rows) into a temp file. The
# python heredoc is a plain redirection on a simple command — NOT inside `$( )` —
# so bash 3.2 parses it correctly regardless of quotes in the detail strings.
DETAIL_FILE="$(mktemp)"
python3 - "$REPORT" >"$DETAIL_FILE" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
rows = []
for f in d.get("features", []):
    if f.get("status") == "fail":
        rows.append("| `{}` | {} passed, {} failed | {} |".format(
            f.get("name", "?"), f.get("passed", 0), f.get("failed", 0),
            (f.get("detail", "") or "").replace("|", "\\|")))
print("\n".join(rows) if rows else "| (no per-feature detail) | | |")
PY

# Assemble the issue body in a temp file with printf. Literal apostrophes and
# (escaped) backticks inside double quotes are safe here — no command
# substitution, no heredoc-in-`$( )` — so this parses on bash 3.2.
BODY_FILE="$(mktemp)"
{
  printf '%s\n\n' "The **${SUITE}** suite failed: **${FAILED} feature(s)** failed, ${PASSED} passed."
  printf '%s\n\n' "Run: ${RUN_URL}"
  printf '%s\n' "| feature | tally | first failure |"
  printf '%s\n' "| --- | --- | --- |"
  cat "$DETAIL_FILE"
  printf '\n%s\n' "_Filed automatically by \`scripts/report-to-issue.sh\` from the run's structured summary. This issue auto-closes when the suite is green again._"
} >"$BODY_FILE"

if [ -n "$EXISTING" ]; then
  gh issue comment "$EXISTING" --body-file "$BODY_FILE" >/dev/null 2>&1 \
    && echo "updated issue #$EXISTING ($FAILED failing features)" \
    || echo "::warning::failed to comment on issue #$EXISTING"
else
  # `gh issue create --label` fails outright if the label doesn't exist — which
  # silently broke the whole report-back channel on a repo that never created it.
  # Ensure it first (--force is idempotent: create or no-op, never errors).
  gh label create "$LABEL" --color C5DEF5 --description "Automated test-suite failure (report-to-issue.sh)" --force >/dev/null 2>&1 || true
  gh issue create --title "$TITLE" --label "$LABEL" --body-file "$BODY_FILE" >/dev/null 2>&1 \
    && echo "opened a new test-failure issue for ${SUITE}" \
    || echo "::warning::failed to open the test-failure issue for ${SUITE}"
fi

# This script is a reporter — it never changes the job's outcome. The suite's own
# exit status (preserved by the caller) is what fails the job.
exit 0
