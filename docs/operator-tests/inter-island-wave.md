# Operator test — Inter-island exchange (Lane 5) live verification

**Run on Minion (real Docker host). This gates the `v0.6.0` release.** ~15 min.
Covers Phases 1–3.5: mailbox → brokered info link → deny-all action gate → wake-on-message.

Conventions below: island **A** = `test-a`, island **B** = `test-b`; `$AG_A` / `$AG_B`
are the agent ids in each (from `dejima agent ls <island>` or `dejima ls`). Run as the
operator on the host unless a step says "inside island A".

## 0. Setup
```bash
# Create two throwaway islands with one agent each (use your normal create flow), then:
dejima ls                       # confirm test-a and test-b are running
dejima agent ls test-a          # note the agent id  → $AG_A
dejima agent ls test-b          # note the agent id  → $AG_B
```

## 1. Deny-all by default (no grant → refused, ledgered)
```bash
dejima link send test-b "$AG_B" "hello" --topic coord --from test-a --from-agent "$AG_A"
#   EXPECT: refused — "no link grant test-a→test-b on topic \"coord\" (cross-island is deny-all)"
dejima audit | grep link.deny   # EXPECT: the refusal is recorded
```

## 2. Grant → cross-island info message lands in B's mailbox, tagged + ledgered
```bash
dejima link grant test-a test-b --topic coord
dejima link ls                                  # EXPECT: the test-a→test-b/coord grant
dejima link send test-b "$AG_B" "hello from A" --topic coord --from test-a --from-agent "$AG_A"
#   EXPECT: delivered

dejima msg poll --island test-b --agent "$AG_B"
#   EXPECT: the message, with origin.source_island=test-a, origin.cross_island=true
dejima audit | grep link.message                # EXPECT: ledgered
```

## 3. Action delegation — deny-all, expose, approve, deny
```bash
# 3a. Unexposed action → refused
dejima link action test-b "$AG_B" deploy --topic coord --from test-a --from-agent "$AG_A"
#   EXPECT: refused — "test-b does not expose action \"deploy\""

# 3b. Operator exposes it
dejima link expose test-b deploy
dejima link exposed test-b                       # EXPECT: [deploy]

# 3c. Request again → not pre-authorized in the grant → PENDING + webhook fires
dejima link action test-b "$AG_B" deploy --topic coord --from test-a --from-agent "$AG_A"
#   EXPECT: status "pending" + an id (act-N); a link.action-pending event/webhook fires
dejima link approvals                            # EXPECT: the pending request listed

# 3d. Approve → executes (delivered as a typed Action into B's mailbox)
dejima link approve act-1                         # use the id from 3c
dejima msg poll --island test-b --agent "$AG_B"  # EXPECT: an Action message (action.type=deploy), cross-island

# 3e. Deny path
dejima link action test-b "$AG_B" deploy --topic coord --from test-a --from-agent "$AG_A"
dejima link deny act-2                            # EXPECT: denied
dejima audit | grep -E 'link.action|link.approve|link.deny'   # EXPECT: all three, with the approver/denier actor
```

## 4. Agent can NEVER self-approve (the boundary)
```bash
# From INSIDE island A (the in-island token path), the approve/grant routes must be denied.
dejima exec test-a -- sh -lc '
  curl -s -o /dev/null -w "%{http_code}\n" -X POST \
    -H "Authorization: Bearer $DEJIMA_TOKEN" \
    "http://$DEJIMA_HOST/v1/link/actions/act-1/approve"'
#   EXPECT: 403/404 — the token listener does not expose approve/grant/expose.
```

## 5. Fail-closed
```bash
# 5a. TTL: create a pending action, wait > the flush/TTL window (default 15m), then approve.
dejima link approve <expired-id>     # EXPECT: 404 "unknown or expired — fail-closed"

# 5b. Restart drops the in-memory queue (nothing auto-executes after a restart).
dejima link action test-b "$AG_B" deploy --topic coord --from test-a --from-agent "$AG_A"  # pending
dejima service restart --system      # (or however you restart dejimad)
dejima link approvals                 # EXPECT: empty
```

## 6. Wake-on-message — the per-adapter live check (UNTESTABLE in CI; the key unknown)
```bash
# 6a. Idle wake: leave B's agent idle (at its prompt / task just completed), then send to it.
dejima link send test-b "$AG_B" "wake check" --topic coord --from test-a --from-agent "$AG_A"
#   EXPECT: within ~moments, the agent's session shows an injected line:
#     "📬 N new message(s) — run: dejima msg poll"
#   CONFIRM: Claude Code actually WAKES and the prompt isn't corrupted.

# 6b. Busy = no mid-turn interrupt. While B's agent is mid-task, send another message.
#   EXPECT: NO injection mid-turn; the nudge arrives at the next turn boundary (≤ ~15s tick).

# 6c. Wake from hibernation.
dejima hibernate test-b
dejima link send test-b "$AG_B" "wake from sleep" --topic coord --from test-a --from-agent "$AG_A"
#   EXPECT: test-b wakes (island.woken event, reason=message), then the nudge delivers.

# 6d. The arrival event leaks no body.
dejima events test-b   # (or your webhook) EXPECT: mailbox.arrival with {from, cross_island, action} only — no payload
```

## 7. Cleanup
```bash
dejima link revoke test-a test-b --topic coord
dejima link unexpose test-b deploy
# purge the throwaway islands (your normal purge flow), e.g.:
dejima rm test-a test-b
```

## Pass criteria → cut `v0.6.0`
All of: deny-all default (1), cross-island delivery tagged + ledgered (2), action
approve/deny gated + audited (3), **agent can't self-approve (4)**, fail-closed on
TTL + restart (5), and **wake-on-message actually wakes Claude Code from the nudge and
from hibernation without clobbering a busy agent (6)**. Then:
```bash
git tag -a v0.6.0 <master-sha> -m "v0.6.0 — inter-island exchange (Lane 5)"   # tag by SHA
git push origin v0.6.0
```
If wake (6) is rough (send-keys corrupts Claude Code's prompt), report back — we swap the
inject seam for a hook-based adapter before tagging.
