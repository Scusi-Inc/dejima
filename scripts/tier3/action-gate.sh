#!/usr/bin/env bash
# Tier-3 ACTION-GATE suite — live, end-to-end proof of the cross-island action
# gate's POLICY DELTA (Lane 5 Phase 3.x: #131/132/134/136/137).
#
# The BASE gate (deny-all, expose, queue, operator approve/deny, ledger) is
# already exercised deterministically by the Tier-2 suite (scripts/integration.sh,
# the "inter-island exchange" feature) and the policy engine is unit-tested
# (internal/policy, internal/api/policy_consume_repro_test.go). What had NO live
# coverage — and what this script proves against a REAL daemon + two REAL islands
# + the REAL CLI + the on-disk ledger — is the operator-opt-in auto-approve policy:
#
#   1. deny-all default — an action with no channel grant is refused.
#   2. exposed-but-unruled MUTATING action → QUEUES for operator approval.
#   3. a scoped/counted policy rule auto-approves WITHIN its budget, then
#      RE-QUEUES once the budget is spent (Used == Max).
#   4. a DESTRUCTIVE action ALWAYS queues — even WITH a matching policy rule
#      (Consume excludes destructive tiers; the rule's budget is never spent).
#   5. every decision is ledgered: policy.add / policy.remove + an auto
#      link.approve (actor=policy); the hash chain still verifies.
#   6. fail-closed — the pending queue is in-memory and is DROPPED on a daemon
#      restart, while persisted policy rules survive.
#
# Runs as the `dejimaqa` test user against its OWN dejimad + colima (a throwaway
# $HOME), never the operator's aoos daemon/islands. Needs colima/Docker for the
# two islands; SKIPS cleanly (not fails) when Docker is unreachable.
#
# Usage:   scripts/tier3/action-gate.sh
# Requires: go, git; colima/Docker for the island-backed checks.

set -uo pipefail
SUITE_NAME="tier3-action-gate"
# shellcheck source=scripts/tier3/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

refuse_as_aoos
build_dejima
trap teardown_daemon EXIT
start_daemon

ISLAND_A="qa-gate-a"
ISLAND_B="qa-gate-b"
trap 'island_cleanup "$ISLAND_A" "$ISLAND_B"; teardown_daemon' EXIT

# The whole suite is island-backed: without colima we can't stand up A/B, so the
# entire body is gated on Docker and SKIPS cleanly (the gate logic itself is
# unit-tested in CI; this script is the LIVE confirmation). report_and_exit runs
# unconditionally at the end and only fails the job on a real assertion failure.
if require_docker "action-gate live suite"; then

  # =========================================================================
  # 0. Two throwaway islands A (requester) and B (target), each a headless idler.
  # =========================================================================
  step "Create two throwaway islands A and B (headless, never aoos's)"
  SEED="$DEJ_TMP/seed"; make_seed_repo "$SEED"
  dejima init --name "$ISLAND_A" --repo "$SEED" --local-copy --agent headless --cmd "sleep infinity" >/dev/null 2>&1 \
    || die "island A create failed (see $(daemon_log))"
  dejima init --name "$ISLAND_B" --repo "$SEED" --local-copy --agent headless --cmd "sleep infinity" >/dev/null 2>&1 \
    || die "island B create failed (see $(daemon_log))"
  B_AGENT="$(dejima agent ls "$ISLAND_B" 2>/dev/null | awk 'NR==2{print $1}')"
  [ -n "$B_AGENT" ] || B_AGENT="a1"
  pass "islands A + B created (B primary agent: $B_AGENT)"

  # A mutating action (default tier) and a destructive one (the name carries a
  # destructive hint — "purge" — so ClassifyTier marks it TierDestructive).
  ACT_MUT="deploy"
  ACT_DESTRUCT="purge-data"

  # =========================================================================
  # 1. deny-all default — no channel grant → the action is refused outright.
  # =========================================================================
  step "deny-all: an action with no link grant is refused"
  expect_fail "action refused before any grant (cross-island is deny-all)" \
    dejima link action "$ISLAND_B" "$B_AGENT" "$ACT_MUT" --from "$ISLAND_A" --topic ops

  # =========================================================================
  # 2. Grant the channel + expose both actions. An exposed-but-unruled MUTATING
  #    action queues for operator approval (prompt-everything default).
  # =========================================================================
  step "Grant A→B/ops and expose the actions"
  expect_ok "grant A→B/ops"            dejima link grant   "$ISLAND_A" "$ISLAND_B" --topic ops
  expect_ok "B exposes $ACT_MUT"       dejima link expose  "$ISLAND_B" "$ACT_MUT"
  expect_ok "B exposes $ACT_DESTRUCT"  dejima link expose  "$ISLAND_B" "$ACT_DESTRUCT"

  step "Default: exposed mutating action with NO policy rule → queues"
  ACT="$(dejima link action "$ISLAND_B" "$B_AGENT" "$ACT_MUT" --from "$ISLAND_A" --topic ops 2>&1)"
  assert_has "$ACT" "queued" "unruled mutating action is queued (prompt-everything)"
  # Drain it so the budget test below starts from a clean queue.
  PID0="$(dejima link approvals 2>/dev/null | awk 'NR==2{print $1}')"
  [ -n "$PID0" ] && dejima link deny "$PID0" --reason "drain before budget test" >/dev/null 2>&1

  # =========================================================================
  # 3. A scoped/counted policy rule auto-approves WITHIN budget, then RE-QUEUES.
  #    max=2 → the first two fire auto, the third falls through to the queue.
  # =========================================================================
  step "Policy: add a counted auto-approve rule (max 2) for the mutating action"
  expect_ok "policy add A→B [$ACT_MUT] max=2" \
    dejima policy add --link "$ISLAND_A->$ISLAND_B" --action "$ACT_MUT" --max 2 --ttl 1h
  RULES="$(dejima policy ls 2>&1)"
  assert_has "$RULES" "$ACT_MUT"    "rule appears in \`policy ls\`"
  assert_has "$RULES" "used 0/2"    "new rule starts unused (0/2)"

  step "Policy: the first two requests auto-approve (within budget)"
  R1="$(dejima link action "$ISLAND_B" "$B_AGENT" "$ACT_MUT" --from "$ISLAND_A" --topic ops 2>&1)"
  assert_has "$R1" "delivered" "1st request auto-approved + delivered (no queue)"
  R2="$(dejima link action "$ISLAND_B" "$B_AGENT" "$ACT_MUT" --from "$ISLAND_A" --topic ops 2>&1)"
  assert_has "$R2" "delivered" "2nd request auto-approved + delivered (budget now spent)"

  RULES2="$(dejima policy ls 2>&1)"
  assert_has "$RULES2" "used 2/2" "rule budget shows fully consumed (2/2)"

  step "Policy: the third request RE-QUEUES (budget exhausted → fail back to operator)"
  R3="$(dejima link action "$ISLAND_B" "$B_AGENT" "$ACT_MUT" --from "$ISLAND_A" --topic ops 2>&1)"
  assert_has "$R3" "queued" "3rd request re-queues once the budget is spent"

  # =========================================================================
  # 4. DESTRUCTIVE always queues — even WITH a matching policy rule. Consume never
  #    matches a destructive tier, so the rule's budget is never spent.
  # =========================================================================
  step "Destructive: a rule canNOT auto-approve a destructive action"
  expect_ok "policy add A→B [$ACT_DESTRUCT] max=5 (operator tries to auto-approve a destructive)" \
    dejima policy add --link "$ISLAND_A->$ISLAND_B" --action "$ACT_DESTRUCT" --max 5 --ttl 1h
  RD="$(dejima link action "$ISLAND_B" "$B_AGENT" "$ACT_DESTRUCT" --from "$ISLAND_A" --topic ops 2>&1)"
  assert_has "$RD" "queued" "destructive action queues despite a matching rule"
  RULES3="$(dejima policy ls 2>&1)"
  # The destructive rule must still read 0/5 — its budget was never touched.
  if printf '%s' "$RULES3" | grep -F "$ACT_DESTRUCT" | grep -qF "used 0/5"; then
    pass "destructive rule budget untouched (0/5 — Consume excluded it)"
  else
    fail "destructive rule budget changed — Consume must never spend it: $(printf '%s' "$RULES3" | grep -F "$ACT_DESTRUCT")"
  fi

  # =========================================================================
  # 5. Ledger — every decision recorded; the hash chain still verifies.
  # =========================================================================
  step "Ledger: policy add + auto-approve are recorded, then policy remove"
  AUDIT="$(dejima audit 2>&1)"
  assert_has "$AUDIT" "policy.add"   "policy.add ledgered"
  assert_has "$AUDIT" "link.approve" "auto-approval ledgered as link.approve"
  expect_ok "policy rm A→B [$ACT_MUT]" \
    dejima policy rm --link "$ISLAND_A->$ISLAND_B" --action "$ACT_MUT"
  AUDIT2="$(dejima audit 2>&1)"
  assert_has "$AUDIT2" "policy.remove" "policy.remove ledgered"
  expect_ok "ledger hash chain verifies with policy.* + link.* entries" dejima audit --verify

  # =========================================================================
  # 6. Fail-closed — the pending queue is in-memory and is DROPPED on a daemon
  #    restart; persisted policy rules survive. (The queue also has a 15-min TTL,
  #    unit-tested in internal/link; the restart drop is the deterministic proof.)
  # =========================================================================
  step "Fail-closed: pending queue is dropped on a daemon restart; rules persist"
  # There are queued actions from steps 3 (re-queued deploy) and 4 (destructive).
  BEFORE="$(dejima link approvals 2>&1)"
  assert_has "$BEFORE" "$ACT_DESTRUCT" "queue holds the pending destructive action before restart"
  # Bounce the daemon on the same HOME/socket (same pattern as safe.sh §2b).
  kill "$DEJ_PID" >/dev/null 2>&1
  for _ in $(seq 1 25); do [ -S "$HOME/.dejima/dejimad.sock" ] || break; sleep 0.2; done
  dejimad --foreground >>"$(daemon_log)" 2>&1 &
  DEJ_PID=$!
  for _ in $(seq 1 50); do [ -S "$HOME/.dejima/dejimad.sock" ] && break; sleep 0.2; done
  if dejima audit >/dev/null 2>&1; then
    pass "daemon came back up after the restart"
    AFTERQ="$(dejima link approvals 2>&1)"
    if printf '%s' "$AFTERQ" | grep -qF "$ACT_DESTRUCT"; then
      fail "pending action survived the restart — the queue must be fail-closed (dropped)"
    else
      pass "pending queue dropped on restart (fail-closed)"
    fi
    RULES4="$(dejima policy ls 2>&1)"
    assert_has "$RULES4" "$ACT_DESTRUCT" "persisted policy rules survive the restart"
  else
    fail "daemon did not recover after the restart"
  fi

fi

report_and_exit
