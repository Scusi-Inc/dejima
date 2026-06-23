#!/usr/bin/env bash
# Onboarding self-test — "dead-simple in under 5 minutes" (Lane 6 Phase C2).
#
# Runs the Mac-mini setup wizard (`dejima onboard --provision-host --yes`) in
# IDEMPOTENT mode (on an already-provisioned host it skips package installs),
# TIMES the automatable flow and asserts it finishes under a 5-minute budget,
# captures every prompt/screen, and has Claude judge: "is this dead-simple?
# where's the friction?" Output is a pass/fail on the time budget PLUS a concrete
# Claude UX critique.
#
# WHY IT'S GATED: `--yes` on a *fresh* host mutates the system (Homebrew, PATH,
# pmset, service install). So the real run is opt-in behind DEJIMA_RUN_SYSTEM=1
# (same gate as scripts/tier3/system.sh) and is intended to run on the runner
# where system.sh has already provisioned the host (making it idempotent). The
# Claude UX critique additionally needs TEST_AGENT_KEY. Missing either gate is a
# SKIP (exit 0), never a failure. Authored + shellcheck-clean here; runs live on
# the Mac-mini runner.
#
# Usage: DEJIMA_RUN_SYSTEM=1 TEST_AGENT_KEY=sk-ant-... scripts/tier3/onboard-selftest.sh
# Env:   DEJIMA_ONBOARD_BUDGET_SECS (default 300), DEJIMA_JUDGE_MODEL (claude-opus-4-8)

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"

DEJIMA_SUITE="${DEJIMA_SUITE:-tier3-onboard-selftest}"
# shellcheck source=scripts/lib/report.sh
. "$REPO_ROOT/scripts/lib/report.sh"
skip(){ printf '  \033[33m∼\033[0m %s (skipped)\n' "$*"; }

BUDGET="${DEJIMA_ONBOARD_BUDGET_SECS:-300}" # 5 minutes
JUDGE_MODEL="${DEJIMA_JUDGE_MODEL:-claude-opus-4-8}"

feature "onboard self-test (timing + UX)"

# --- gates -----------------------------------------------------------------
if [ "${DEJIMA_RUN_SYSTEM:-0}" != "1" ]; then
  skip "DEJIMA_RUN_SYSTEM!=1 — onboard --provision-host can mutate the host; opt in to run it"
  report_summary || true
  exit 0
fi
if ! command -v go >/dev/null 2>&1; then
  skip "go not found — cannot build dejima"
  report_summary || true
  exit 0
fi

# --- build -----------------------------------------------------------------
TMP="$(mktemp -d)"
BIN="$TMP/bin"; mkdir -p "$BIN"
cleanup(){ rm -rf "$TMP"; }
trap cleanup EXIT
if ( cd "$REPO_ROOT" && go build -o "$BIN/dejima" ./cmd/dejima ); then
  pass "dejima built"
else
  fail "build failed"; report_summary || true; exit 1
fi

# --- timed, idempotent run -------------------------------------------------
# --yes runs only the scriptable steps and collects GUI/off-box pauses into a
# checklist. On an already-provisioned host this is a no-op-ish re-run, so it
# measures the wizard's own flow rather than a from-scratch install. We capture
# all output (the prompts/checklist) for the Claude critique.
OUT="$TMP/onboard.out"
start="$(date +%s)"
# `sudo -n` (non-interactive) so a missing sudo rule fails fast instead of
# hanging on a password prompt; the runner grants scoped passwordless sudo. The
# capture file is created by THIS (unprivileged) shell, so pipe sudo's combined
# output through `tee` rather than redirecting after sudo (which wouldn't apply).
if sudo -n "$BIN/dejima" onboard --provision-host --yes 2>&1 | tee "$OUT"; then
  rc=0
else
  rc=${PIPESTATUS[0]}
fi
end="$(date +%s)"
elapsed=$((end - start))

# A re-run on an already-provisioned host must succeed (idempotent). A non-zero
# rc here is a real regression in the wizard's idempotency.
if [ "$rc" -eq 0 ]; then
  pass "onboard --provision-host --yes re-ran cleanly (idempotent, rc=0)"
else
  fail "onboard --provision-host --yes failed on re-run (rc=$rc) — see captured output"
fi

# Time budget: the automatable flow must finish under DEJIMA_ONBOARD_BUDGET_SECS.
if [ "$elapsed" -le "$BUDGET" ]; then
  pass "onboard flow finished in ${elapsed}s (budget ${BUDGET}s)"
else
  fail "onboard flow took ${elapsed}s — over the ${BUDGET}s budget"
fi

# It must NOT re-install packages on an already-provisioned host (idempotency).
if grep -qiE "installing (homebrew|tailscale|docker)|downloading .* (brew|colima)" "$OUT"; then
  fail "onboard appears to RE-install packages on an already-provisioned host (not idempotent)"
else
  pass "onboard did not re-install packages (idempotent)"
fi

# --- Claude UX critique ----------------------------------------------------
if [ -z "${TEST_AGENT_KEY:-}" ]; then
  skip "TEST_AGENT_KEY not set — skipping the Claude 'dead-simple?' UX critique"
  report_summary || { printf '\033[31mONBOARD SELF-TEST FAILED\033[0m\n'; exit 1; }
  printf '\033[32mONBOARD SELF-TEST: ALL GREEN\033[0m\n'
  exit 0
fi

feature "onboard self-test: Claude UX critique"
PROMPT="You are a developer-experience reviewer. Below is the full terminal output of
a Mac setup wizard (\`dejima onboard --provision-host --yes\`), which is supposed
to turn a fresh Mac into an agent server in one command. It finished in ${elapsed}s.

Judge the ONBOARDING UX from this output. Reply with STRICT JSON, no prose, no
code fence:
{\"dead_simple\": true|false, \"friction\": [\"...\",\"...\"], \"summary\": \"one sentence\"}
dead_simple=true means a non-expert could follow it without confusion: clear
steps, clear next actions, no unexplained jargon, the GUI/manual checklist (if
any) is actionable. friction is a list of concrete friction points or confusing
prompts (empty list if none). Be specific and fair.

--- ONBOARD OUTPUT START ---
$(cat "$OUT")
--- ONBOARD OUTPUT END ---"

body="$(JUDGE_MODEL="$JUDGE_MODEL" PROMPT="$PROMPT" python3 - <<'PY'
import json, os
print(json.dumps({
    "model": os.environ["JUDGE_MODEL"],
    "max_tokens": 512,
    "messages": [{"role": "user", "content": os.environ["PROMPT"]}],
}))
PY
)"

resp="$(curl -sS https://api.anthropic.com/v1/messages \
  -H "x-api-key: $TEST_AGENT_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d "$body" 2>/dev/null)"

verdict="$(RESP="$resp" python3 - <<'PY'
import json, os, re, sys
try:
    data = json.loads(os.environ["RESP"])
except Exception:
    print("ERR\tcould not parse API response"); sys.exit(0)
if "content" not in data:
    msg = (data.get("error") or {}).get("message", "no content")
    print("ERR\t" + msg); sys.exit(0)
text = "".join(b.get("text", "") for b in data["content"] if b.get("type") == "text")
m = re.search(r"\{.*\}", text, re.S)
if not m:
    print("ERR\tno JSON verdict in reply"); sys.exit(0)
try:
    v = json.loads(m.group(0))
except Exception:
    print("ERR\tverdict not valid JSON"); sys.exit(0)
fr = v.get("friction") or []
fr = "; ".join(str(x) for x in fr) if isinstance(fr, list) else str(fr)
print(("SIMPLE" if v.get("dead_simple") else "FRICTION") + "\t" + str(v.get("summary","")) + "\t" + fr)
PY
)"

IFS=$'\t' read -r tag summary friction <<EOF
$verdict
EOF
case "$tag" in
  SIMPLE)
    pass "Claude: dead-simple — $summary"
    [ -n "$friction" ] && printf '    minor friction noted: %s\n' "$friction"
    ;;
  FRICTION)
    # The UX critique is advisory — surface it loudly but DON'T fail the job on
    # subjective friction (the time-budget + idempotency checks are the hard
    # gates). Print it so the operator + the issue reporter capture it.
    pass "Claude UX critique captured (advisory)"
    printf '    \033[33mClaude flags friction:\033[0m %s\n' "$summary"
    [ -n "$friction" ] && printf '    friction points: %s\n' "$friction"
    ;;
  *)
    skip "Claude UX critique unavailable (${summary:-$friction})"
    ;;
esac

report_summary || { printf '\033[31mONBOARD SELF-TEST FAILED\033[0m\n'; exit 1; }
printf '\033[32mONBOARD SELF-TEST: ALL GREEN\033[0m\n'
