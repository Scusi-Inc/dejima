#!/usr/bin/env bash
# Live TUI-drive + Claude screen-analysis (Lane 6 Phase C2).
#
# Walks the REAL `dejima` TUI in a tmux pane — sends keystrokes with
# `tmux send-keys`, captures each rendered frame with `tmux capture-pane`, and
# feeds the frame to Claude (the Messages API) as an LLM-as-UX-judge: is the
# screen correct, clear, glitch-free; does each action do what its label says?
# This is the live counterpart to the teatest unit suite (which asserts fixed
# frames); Claude catches clarity/rendering regressions a fixed assertion misses.
#
# DATA SOURCE: `dejima tui --demo` — a synthetic fleet baked into the binary
# (tui_demo.go), so the harness needs NO daemon and NO Docker. (It previously
# built a throwaway dejimad and seeded islands via `dejima init`; that broke once
# `dejima init` started building the island image up front — no Docker on this
# job → empty dashboard → every island-dependent judge failed. --demo renders the
# real TUI with rich, reproducible content, which is exactly what the judge needs.)
#
# GATING: requires TEST_AGENT_KEY (the Claude API key) AND tmux/curl/python3.
# Missing any is a SKIP (exit 0), never a failure — so a partially provisioned
# runner stays green. No daemon, no system mutation. Designed for the Mac-mini
# runner; authored + shellcheck-clean here (no TTY/tmux in the build island).
#
# Usage:  TEST_AGENT_KEY=sk-ant-... scripts/tier3/tui-claude.sh
# Env:    DEJIMA_JUDGE_MODEL (default claude-opus-4-8), DEJIMA_REPORT (JSON out)

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"

# Reporting + tally helpers (feature/pass/fail/report_summary).
DEJIMA_SUITE="${DEJIMA_SUITE:-tier3-tui-claude}"
# report.sh is sourced at runtime; source=/dev/null keeps shellcheck (run
# without -x in CI) from flagging SC1091.
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/lib/report.sh"
skip(){ printf '  \033[33m∼\033[0m %s (skipped)\n' "$*"; }

# The Claude model that judges each frame. Opus 4.8 is the default per the
# claude-api guidance; override with DEJIMA_JUDGE_MODEL.
JUDGE_MODEL="${DEJIMA_JUDGE_MODEL:-claude-opus-4-8}"
PANE="dejima-tui-test"

# --- prerequisites (skip, don't fail) --------------------------------------
if [ -z "${TEST_AGENT_KEY:-}" ]; then
  feature "tui-claude screen analysis"
  skip "TEST_AGENT_KEY not set — cannot call Claude to judge frames"
  report_summary || true
  exit 0
fi
for bin in tmux curl python3 go git; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    feature "tui-claude screen analysis"
    skip "$bin not found — cannot drive the live TUI"
    report_summary || true
    exit 0
  fi
done

# --- isolated environment + teardown ---------------------------------------
TMP="$(mktemp -d)"
export HOME="$TMP/home"; mkdir -p "$HOME"
BIN="$TMP/bin"; mkdir -p "$BIN"; export PATH="$BIN:$PATH"
# --demo never dials a daemon, but the client still resolves a socket path from
# HOME/DEJIMA_HOST — clear any inherited island-autonomy env so it stays local.
unset DEJIMA_HOST DEJIMA_TOKEN DEJIMA_PROJECT_NAME DEJIMA_AGENT_ID 2>/dev/null || true
cleanup(){
  set +e
  tmux kill-session -t "$PANE" >/dev/null 2>&1
  chmod -R u+w "$TMP" 2>/dev/null
  rm -rf "$TMP"
}
trap cleanup EXIT

# --- build the client ------------------------------------------------------
feature "tui-claude: build"
if ( cd "$REPO_ROOT" && go build -o "$BIN/dejima" ./cmd/dejima ); then
  pass "dejima built"
else
  fail "build failed"; report_summary || true; exit 1
fi

# --- helpers: drive + capture + judge --------------------------------------

# launch_tui starts the real TUI in --demo mode inside a detached tmux pane.
launch_tui(){
  tmux kill-session -t "$PANE" >/dev/null 2>&1
  tmux new-session -d -s "$PANE" -x 120 -y 40
  tmux send-keys -t "$PANE" "HOME=$HOME PATH=$PATH dejima tui --demo" Enter
  sleep 2
}

# send <keys...> presses keys in the pane (tmux send-keys syntax).
send(){ tmux send-keys -t "$PANE" "$@"; sleep 1; }

# capture prints the current rendered frame (visible pane text).
capture(){ tmux capture-pane -t "$PANE" -p; }

# judge <screen-name> <expectation> <frame-text>: ask Claude whether the frame is
# correct/clear/glitch-free for the named screen. Claude replies with a strict
# JSON verdict; a "pass":true verdict passes the check, otherwise it fails with
# Claude's reason. Any API/parse error is a SKIP (don't fail CI on a flaky call).
judge(){
  local screen="$1" expect="$2" frame="$3"
  local prompt body resp verdict reason
  prompt="You are a UX reviewer for a terminal UI (a TUI). Below is a captured
frame of the screen named \"$screen\". Expectation: $expect

Judge ONLY what is visible. Reply with STRICT JSON, no prose, no code fence:
{\"pass\": true|false, \"reason\": \"one sentence\"}
pass=true means the screen is correct, clear, and free of rendering glitches
(no overlapping text, no truncated borders, no stray escape codes) and matches
the expectation. pass=false means a real problem a user would notice.

--- FRAME START ---
$frame
--- FRAME END ---"

  # Build the request body with python3 (robust JSON escaping of the frame).
  body="$(JUDGE_MODEL="$JUDGE_MODEL" PROMPT="$prompt" python3 - <<'PY'
import json, os
print(json.dumps({
    "model": os.environ["JUDGE_MODEL"],
    "max_tokens": 256,
    "messages": [{"role": "user", "content": os.environ["PROMPT"]}],
}))
PY
)"

  resp="$(curl -sS https://api.anthropic.com/v1/messages \
    -H "x-api-key: $TEST_AGENT_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" \
    -d "$body" 2>/dev/null)"

  # Extract the model's text, then parse the JSON verdict out of it.
  verdict="$(RESP="$resp" python3 - <<'PY'
import json, os, re, sys
try:
    data = json.loads(os.environ["RESP"])
except Exception:
    print("ERR\tcould not parse API response"); sys.exit(0)
if "content" not in data:
    msg = (data.get("error") or {}).get("message", "no content in API response")
    print("ERR\t" + msg); sys.exit(0)
text = "".join(b.get("text", "") for b in data["content"] if b.get("type") == "text")
m = re.search(r"\{.*\}", text, re.S)
if not m:
    print("ERR\tno JSON verdict in model reply"); sys.exit(0)
try:
    v = json.loads(m.group(0))
except Exception:
    print("ERR\tverdict was not valid JSON"); sys.exit(0)
print(("PASS" if v.get("pass") else "FAIL") + "\t" + str(v.get("reason", "")))
PY
)"
  reason="${verdict#*$'\t'}"
  case "$verdict" in
    PASS*) pass "$screen — $reason" ;;
    FAIL*) fail "$screen — Claude flagged: $reason" ;;
    *)     skip "$screen — judge unavailable ($reason)" ;;
  esac
}

# --- walk the screens ------------------------------------------------------
feature "tui-claude: dashboard + navigation"
launch_tui
judge "island list" "lists several islands (the demo fleet: storefront, api-gateway, infra, docs-site) with a clear header and selectable rows; agent states read cleanly" "$(capture)"
send "j"; send "k"
judge "after j/k navigation" "the selection cursor is visible and navigation did not corrupt the layout" "$(capture)"

feature "tui-claude: action menu (m)"
send "m"
judge "action menu" "a centered pop-up menu lists lifecycle actions (Hibernate/Reset/Upgrade/Purge); not overlapping the list behind it" "$(capture)"
send Escape

feature "tui-claude: purge confirm (type-the-name gate)"
send "d"
judge "purge confirm" "a confirm pop-up asks the user to TYPE the island name; the destructive action is clearly gated" "$(capture)"
send Escape

feature "tui-claude: action-gate approvals (V)"
send "!"   # demo-only: stage pending cross-island actions (incl. a destructive one)
send "V"
judge "approvals overlay" "a pending cross-island actions list with risk tiers; the destructive row is visually distinct (red/warning); clear approve/deny affordances; no glitches" "$(capture)"
send Escape

feature "tui-claude: help overlay (?)"
send "?"
judge "help overlay" "a help screen explains the keybindings clearly and readably" "$(capture)"
send Escape

feature "tui-claude: audit pane (A)"
send "A"
judge "audit pane" "an audit/ledger view renders without glitches (it may show an error if no daemon audit — that is acceptable if clearly labelled)" "$(capture)"
send Escape

send "q"

# --- summary ---------------------------------------------------------------
report_summary || { printf '\033[31mTUI-CLAUDE SCREEN ANALYSIS FAILED\033[0m\n'; exit 1; }
printf '\033[32mTUI-CLAUDE SCREEN ANALYSIS: ALL GREEN\033[0m\n'
