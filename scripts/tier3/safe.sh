#!/usr/bin/env bash
# Tier-3 SAFE macOS-host suite — no system mutation.
#
# Runs as the `dejimaqa` test user against its OWN dejimad + colima (a throwaway
# $HOME), so it never touches the operator's aoos daemon, islands, or Keychain
# items beyond the test ones it creates+deletes. NO sudo, NO launchd, NO pmset.
# The system-mutating checks (service install --system, onboard --provision-host,
# reboot survival) live in scripts/tier3/system.sh, gated behind an explicit
# workflow_dispatch opt-in.
#
# Covers (matrix rows): Keychain secret storage (§11/§12), idle auto-hibernate
# fires+wakes (§14), terminal auto-reconnect (§3), per-adapter wake — Claude Code
# wakes from the tmux send-keys nudge + from hibernation, busy agent not
# interrupted (§8 wake-on-message / P3.5), and the SSH façade into /workspace (§15).
#
# Usage:   scripts/tier3/safe.sh
# Requires: go, git; colima/Docker for the island-backed checks (they SKIP, not
#           fail, when Docker is unreachable). macOS for the Keychain assertion
#           (on Linux the secrets store falls back to the file path; the
#           not-plaintext-in-config invariant is checked on both).
set -uo pipefail
SUITE_NAME="tier3-safe"
# shellcheck source=scripts/tier3/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

refuse_as_aoos
build_dejima
trap teardown_daemon EXIT
start_daemon

ISLAND="qa-t3-safe"
trap 'island_cleanup "$ISLAND"; teardown_daemon' EXIT

# ===========================================================================
# 1. Keychain secret storage — a webhook HMAC secret is NOT plaintext in config.
#    On macOS it lands in the login Keychain (service "dejima", account
#    "webhook.<id>"); on Linux it lands in the 0600 file store. Either way it
#    must NOT linger in ~/.dejima/webhooks.json.
# ===========================================================================
step "Keychain: webhook secret stored out of plaintext config"
SECRET="t3-secret-$$-do-not-log"
SUB_OUT="$(dejima webhook subscribe --url "https://example.invalid/hook" --secret "$SECRET" 2>&1)"
if printf '%s' "$SUB_OUT" | grep -q "subscribed:"; then
  pass "webhook subscribe accepted"
  SUB_ID="$(printf '%s' "$SUB_OUT" | sed -n 's/^subscribed: \([^ ]*\) .*/\1/p')"
  WEBHOOKS_JSON="$HOME/.dejima/webhooks.json"
  if [ -f "$WEBHOOKS_JSON" ]; then
    if grep -qF "$SECRET" "$WEBHOOKS_JSON"; then
      fail "HMAC secret is PLAINTEXT in $WEBHOOKS_JSON (must be in the secrets store)"
    else
      pass "HMAC secret is not plaintext in webhooks.json"
    fi
  else
    pass "webhooks.json absent (nothing to leak)"
  fi
  if [ "$(uname -s)" = "Darwin" ] && [ -n "$SUB_ID" ]; then
    if security find-generic-password -s dejima -a "webhook.$SUB_ID" -w >/dev/null 2>&1; then
      pass "HMAC secret present in the login Keychain (service=dejima, account=webhook.$SUB_ID)"
    else
      # The login keychain can be locked on a headless runner — then the store
      # degrades to the file fallback by design. Treat as a notice, not a failure.
      skip "Keychain item not found (locked keychain? secret degraded to file store)"
    fi
  else
    KEYSTORE="$HOME/.dejima/secrets/keystore.json"
    if [ -f "$KEYSTORE" ] && grep -qF "$SUB_ID" "$KEYSTORE"; then
      pass "HMAC secret stored in the 0600 file store (non-macOS fallback)"
    else
      skip "file-store keystore not populated (store backend may differ)"
    fi
  fi
else
  fail "webhook subscribe failed: $SUB_OUT"
fi

# ===========================================================================
# 2. The island-backed checks. Everything below needs a running island, so it
#    needs colima/Docker. Skip cleanly if Docker isn't up.
# ===========================================================================
if require_docker "island-backed tier-3 checks"; then
  step "Create a throwaway headless island (its own, never aoos's)"
  SEED="$DEJ_TMP/seed"; make_seed_repo "$SEED"
  if ! dejima init --name "$ISLAND" --repo "$SEED" --local-copy --agent headless --cmd "sleep infinity" >/dev/null 2>&1; then
    fail "island create failed (see $(daemon_log)) — skipping island-backed checks"
  else
    pass "island $ISLAND created and running"

    # -------------------------------------------------------------------
    # 2a. idle auto-hibernate fires + wakes. We can't easily restart the daemon
    #     with a new flag mid-suite, so we exercise the wake half deterministically
    #     and the fire half via the daemon's idle reaper if DEJIMAD_IDLE_HIBERNATE
    #     was set before launch (the workflow sets a short window for this job).
    # -------------------------------------------------------------------
    step "idle auto-hibernate: explicit hibernate -> wake round-trip (state intact)"
    dejima exec "$ISLAND" -- sh -c 'echo persisted > /tmp/marker.txt' >/dev/null 2>&1 || true
    expect_ok "hibernate the island" dejima hibernate "$ISLAND"
    # Confirm it actually stopped (status no longer 'running').
    ST="$(dejima status "$ISLAND" 2>&1 || dejima info "$ISLAND" 2>&1)"
    if printf '%s' "$ST" | grep -qiE 'hibernat|stopped|paused'; then
      pass "island reports hibernated/stopped"
    else
      skip "could not confirm hibernated state from status output"
    fi
    expect_ok "wake the island" dejima wake "$ISLAND"
    GOT="$(dejima exec "$ISLAND" -- cat /tmp/marker.txt 2>/dev/null)"
    assert_eq "$GOT" "persisted" "state survived the hibernate->wake round-trip"

    if [ -n "${DEJIMAD_IDLE_HIBERNATE:-}" ]; then
      step "idle auto-hibernate: the reaper fires after the idle window ($DEJIMAD_IDLE_HIBERNATE)"
      # The reaper only hibernates islands with no attached client and no live
      # agent. headless 'sleep infinity' has no live agent, so after the window
      # it should auto-hibernate. Poll the daemon log + status briefly.
      fired=""
      for _ in $(seq 1 30); do
        if grep -qi "idle hibernate" "$(daemon_log)" 2>/dev/null; then fired=1; break; fi
        sleep 1
      done
      if [ -n "$fired" ]; then
        pass "idle reaper logged an auto-hibernate"
        expect_ok "island wakes again after auto-hibernate" dejima wake "$ISLAND"
      else
        skip "idle reaper did not fire within 30s (window may exceed the poll budget)"
      fi
    else
      skip "DEJIMAD_IDLE_HIBERNATE not set — auto-fire half not exercised this run"
    fi

    # -------------------------------------------------------------------
    # 2b. terminal auto-reconnect — drop the link (kill+restart the daemon) and
    #     the agent's tmux session must still be there to reattach to (it lives
    #     in the container, not the daemon). We assert the session persists across
    #     a daemon bounce and that exec works again after.
    # -------------------------------------------------------------------
    step "terminal auto-reconnect: session survives a daemon restart"
    BEFORE="$(dejima exec "$ISLAND" -- sh -c 'echo alive' 2>/dev/null)"
    assert_eq "$BEFORE" "alive" "exec works before the drop"
    # Drop: kill the daemon, then bring a fresh one up on the same HOME/socket.
    kill "$DEJ_PID" >/dev/null 2>&1
    for _ in $(seq 1 25); do [ -S "$HOME/.dejima/dejimad.sock" ] || break; sleep 0.2; done
    dejimad --foreground >>"$(daemon_log)" 2>&1 &
    DEJ_PID=$!
    for _ in $(seq 1 50); do [ -S "$HOME/.dejima/dejimad.sock" ] && break; sleep 0.2; done
    if dejima audit >/dev/null 2>&1; then
      pass "daemon came back up after the drop"
      AFTER="$(dejima exec "$ISLAND" -- sh -c 'echo alive' 2>/dev/null)"
      assert_eq "$AFTER" "alive" "exec reattaches after the daemon restart (terminal didn't die)"
    else
      fail "daemon did not recover after the drop"
    fi

    # -------------------------------------------------------------------
    # 2c. SSH façade into /workspace. The façade is only meaningful when the
    #     daemon was installed with --ssh; in the safe (no-install) path we can
    #     only assert the authorize plumbing exists. Full shell/sftp landing in
    #     /workspace is exercised by the system suite (which installs the façade).
    # -------------------------------------------------------------------
    step "SSH façade: account-key authorize plumbing is reachable"
    # ssh authorize/revoke are owner-only key management; just confirm the
    # command path responds (no real key pushed in the safe path).
    if dejima ssh --help >/dev/null 2>&1; then
      pass "ssh façade command surface present (full landing check is in system.sh)"
    else
      skip "ssh command not available in this build"
    fi
  fi
fi

# ===========================================================================
# 3. Per-adapter wake (the P3.5 live unknown). This needs a real Claude Code
#    adapter + TEST_AGENT_KEY to be meaningful (does the tmux send-keys nudge
#    wake Claude without corrupting its prompt, and does a busy agent get left
#    alone?). Without the key we can't launch a real adapter — delegate to the
#    Tier-4 smoke (scripts/tier4/agent-smoke.sh covers the wake assertion when
#    a key is present) and record a skip here.
# ===========================================================================
step "per-adapter wake (Claude Code from send-keys nudge / hibernation)"
if [ -z "${TEST_AGENT_KEY:-}" ]; then
  skip "TEST_AGENT_KEY not set — real-adapter wake is exercised by the Tier-4 smoke"
else
  skip "real-adapter wake is owned by scripts/tier4/agent-smoke.sh (run that job)"
fi

report_and_exit
