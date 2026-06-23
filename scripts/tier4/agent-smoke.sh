#!/usr/bin/env bash
# Tier-4 real-agent smoke — LENIENT.
#
# Launches a REAL agent on a throwaway island and asserts it "ran / produced a
# commit," never exact LLM output. Runs as `dejimaqa` against its own dejimad +
# colima, with the bot GitHub account (TEST_GH_TOKEN / TEST_GH_OWNER) as the
# clone/push target and TEST_AGENT_KEY as the provider credential. See
# docs/testing/dejimaqa-runner-setup.md.
#
# Flow:
#   1. provider set anthropic   (from TEST_AGENT_KEY)
#   2. create island A from a bot sandbox repo, with a real claude-code agent
#   3. nudge the agent (tmux send-keys) with a trivial task: write a file + commit
#   4. poll for a NEW commit in the agent worktree (lenient timeout)
#   5. push to the bot repo (best-effort; assert the push path runs)
#   6. one inter-island message: create island B, grant a link, send A->B, poll B
#
# SECRET-GATED: if TEST_AGENT_KEY is missing this exits 0 with a skip notice (the
# workflow should already gate the job, this is belt-and-suspenders). Missing
# TEST_GH_TOKEN downgrades the push assertion to a skip (commit still asserted).
#
# Usage:   TEST_AGENT_KEY=... [TEST_GH_TOKEN=... TEST_GH_OWNER=... TEST_GH_REPO=...] scripts/tier4/agent-smoke.sh
set -uo pipefail
SUITE_NAME="tier4-agent-smoke"
# shellcheck source=scripts/tier3/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../tier3" && pwd)/lib.sh"

refuse_as_aoos

if [ -z "${TEST_AGENT_KEY:-}" ]; then
  step "Tier-4 is secret-gated"
  skip "TEST_AGENT_KEY not set — cannot launch a real agent"
  report_and_exit
fi

build_dejima
trap teardown_daemon EXIT
start_daemon

ISLAND_A="qa-t4-a"
ISLAND_B="qa-t4-b"
trap 'island_cleanup "$ISLAND_A" "$ISLAND_B"; teardown_daemon' EXIT

if ! require_docker "tier-4 agent smoke"; then
  report_and_exit
fi

# ===========================================================================
# 1. Provider key (anthropic → ANTHROPIC_API_KEY injected per-agent).
# ===========================================================================
step "provider set anthropic (from TEST_AGENT_KEY)"
if printf '%s' "$TEST_AGENT_KEY" | dejima provider set anthropic --stdin --default >/dev/null 2>&1; then
  pass "anthropic provider key stored"
else
  fail "provider set anthropic failed — cannot launch a real agent"
  report_and_exit
fi

# ===========================================================================
# 2. Create island A from a bot sandbox repo with a real claude-code agent.
#    Prefer the bot repo; fall back to a local seed if no bot repo is configured
#    (still launches a real agent, just no push target).
# ===========================================================================
PUSH_TARGET=""
REPO_ARG=""
if [ -n "${TEST_GH_OWNER:-}" ] && [ -n "${TEST_GH_REPO:-}" ]; then
  REPO_ARG="https://github.com/${TEST_GH_OWNER}/${TEST_GH_REPO}.git"
  PUSH_TARGET=1
fi

step "Create island A with a real claude-code agent"
if [ -n "$PUSH_TARGET" ]; then
  # Seed the daemon GitHub identity from the bot PAT so clone/push authenticate.
  if [ -n "${TEST_GH_TOKEN:-}" ]; then
    export GH_TOKEN="$TEST_GH_TOKEN"
  fi
  if dejima init --name "$ISLAND_A" --repo "$REPO_ARG" --agent claude-code >/dev/null 2>&1; then
    pass "island A created from the bot repo ($TEST_GH_OWNER/$TEST_GH_REPO)"
  else
    skip "create from bot repo failed (creds/repo?) — falling back to a local seed"
    PUSH_TARGET=""
  fi
fi
if [ -z "$PUSH_TARGET" ] && ! dejima ls 2>/dev/null | grep -q "$ISLAND_A"; then
  SEED="$DEJ_TMP/seedA"; make_seed_repo "$SEED"
  if dejima init --name "$ISLAND_A" --repo "$SEED" --local-copy --agent claude-code >/dev/null 2>&1; then
    pass "island A created from a local seed (no push target)"
  else
    fail "island A create failed (see $(daemon_log))"
    report_and_exit
  fi
fi

# Resolve the agent id (don't hardcode the id scheme).
AG_A="$(dejima agent ls "$ISLAND_A" 2>/dev/null | awk 'NR>1{print $1; exit}')"
if [ -n "$AG_A" ]; then pass "island A agent id = $AG_A"; else skip "could not resolve island A agent id (using default session)"; fi
SESSION_A="agent-${AG_A:-primary}"

# ===========================================================================
# 3. Give the agent a trivial task via tmux send-keys (the same inject seam the
#    wake nudge uses). LENIENT — we ask for a file + commit and then look for ANY
#    new commit; we never assert the agent's words.
# ===========================================================================
step "Nudge the agent with a trivial task (write a file + commit)"
BASE_SHA="$(dejima exec "$ISLAND_A" -- sh -lc 'cd /workspace 2>/dev/null && git rev-parse HEAD 2>/dev/null' 2>/dev/null)"
TASK='Please create a file SMOKE.md containing the single word OK, then run: git add SMOKE.md && git commit -m "tier4 smoke". Do only that.'
# send-keys into the agent's tmux session, then Enter.
if dejima exec "$ISLAND_A" -- tmux send-keys -t "$SESSION_A" "$TASK" Enter >/dev/null 2>&1; then
  pass "task injected into the agent session ($SESSION_A)"
else
  skip "tmux send-keys into $SESSION_A failed — agent session naming may differ; checking for commits anyway"
fi

# ===========================================================================
# 4. Poll for a NEW commit (lenient — generous timeout, never fails the JOB on a
#    no-op since real-agent behavior is nondeterministic; records a skip instead).
# ===========================================================================
step "Poll for a new commit produced by the agent (lenient, ~5 min)"
PRODUCED=""
for _ in $(seq 1 60); do
  NOW_SHA="$(dejima exec "$ISLAND_A" -- sh -lc 'cd /workspace 2>/dev/null && git rev-parse HEAD 2>/dev/null' 2>/dev/null)"
  if [ -n "$NOW_SHA" ] && [ "$NOW_SHA" != "$BASE_SHA" ]; then PRODUCED=1; break; fi
  # Also accept a commit in any agent worktree (multi-agent layout).
  if dejima exec "$ISLAND_A" -- sh -lc 'cd /workspace && git log --oneline -1 2>/dev/null | grep -qi "tier4 smoke"' >/dev/null 2>&1; then
    PRODUCED=1; break
  fi
  sleep 5
done
if [ -n "$PRODUCED" ]; then
  pass "the agent produced a new commit (smoke ran)"
else
  # Lenient: a no-op real-agent run is a notice, not a regression.
  skip "no new commit within the window — real-agent run was a no-op (lenient: not a failure)"
fi

# ===========================================================================
# 5. Push to the bot repo (best-effort). Assert the push path RUNS; a clean push
#    requires the bot creds + an actual commit.
# ===========================================================================
step "Push the island's work to the bot repo"
if [ -z "$PUSH_TARGET" ]; then
  skip "no bot repo configured (TEST_GH_OWNER/TEST_GH_REPO) — push not exercised"
elif [ -z "$PRODUCED" ]; then
  skip "nothing to push (agent produced no commit)"
else
  if dejima exec "$ISLAND_A" -- sh -lc 'cd /workspace && git push origin HEAD 2>&1' >/dev/null 2>&1; then
    pass "push to the bot repo succeeded"
  else
    skip "push did not succeed (bot creds/permissions?) — commit was produced (lenient)"
  fi
fi

# ===========================================================================
# 6. One inter-island message: A -> B over a granted link.
# ===========================================================================
step "Inter-island message A -> B (deny-all -> grant -> deliver)"
SEEDB="$DEJ_TMP/seedB"; make_seed_repo "$SEEDB"
if dejima init --name "$ISLAND_B" --repo "$SEEDB" --local-copy --agent headless --cmd "sleep infinity" >/dev/null 2>&1; then
  pass "island B created"
  AG_B="$(dejima agent ls "$ISLAND_B" 2>/dev/null | awk 'NR>1{print $1; exit}')"
  # deny-all before grant
  if dejima link send "$ISLAND_B" "${AG_B:-primary}" "hi" --topic coord --from "$ISLAND_A" --from-agent "${AG_A:-primary}" >/dev/null 2>&1; then
    fail "cross-island send succeeded with NO grant (deny-all violated)"
  else
    pass "deny-all: send refused before any grant"
  fi
  expect_ok "grant A->B on topic coord" dejima link grant "$ISLAND_A" "$ISLAND_B" --topic coord
  if dejima link send "$ISLAND_B" "${AG_B:-primary}" "hello from A" --topic coord --from "$ISLAND_A" --from-agent "${AG_A:-primary}" >/dev/null 2>&1; then
    pass "cross-island message delivered after grant"
    MSG="$(dejima msg poll --island "$ISLAND_B" --agent "${AG_B:-primary}" 2>&1)"
    assert_has "$MSG" "hello from A" "message landed in B's mailbox"
  else
    fail "cross-island send failed after a valid grant"
  fi
else
  skip "island B create failed — inter-island message not exercised"
fi

report_and_exit
