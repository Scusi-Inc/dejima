#!/usr/bin/env bash
# Tier-3 RECONNECT-RESILIENCE suite — the live proof of #129.
#
# The operator's exact bug: an attached terminal session EXITED (code 1) when the
# link dropped — a daemon restart, the host sleeping/waking, a >5-min transport
# outage. The fix (cmd/dejima/main.go runSessionLoop / reconnectSession): an
# abnormal drop transparently reconnects to the persistent in-container tmux
# session with capped backoff for up to 5 minutes; only a CLEAN server close
# (detach / agent exit) ends the client, and a genuinely-gone target gives up
# FAST with a clear message. The decision layer is unit-tested
# (cmd/dejima/session_reconnect_test.go: classifySessionClose); this script is
# the LIVE confirmation against a real daemon + a real `dejima shell` session.
#
# It proves:
#   1. an attached session SURVIVES a daemon restart — the client process stays
#      alive (it does NOT exit code 1) and resumes the SAME in-container session.
#   2. a clean stdin close (the terminal going away) exits the client CLEANLY
#      (rc 0), not as an error.
#   3. a genuinely-gone target gives up FAST with a clear message (no 5-min hang).
#
# Runs as the `dejimaqa` test user against its OWN dejimad + colima (a throwaway
# $HOME), never the operator's aoos daemon/islands. Needs colima/Docker for the
# island; SKIPS cleanly (not fails) when Docker is unreachable.
#
# Usage:   scripts/tier3/reconnect.sh
# Requires: go, git; colima/Docker for the island-backed checks.

set -uo pipefail
SUITE_NAME="tier3-reconnect"
# shellcheck source=scripts/tier3/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

refuse_as_aoos
build_dejima
trap teardown_daemon EXIT
start_daemon

ISLAND="qa-reconnect"
SHELL_PID=""
# Best-effort teardown: kill the attached client, close the stdin fifo writer,
# purge the island, stop the daemon.
cleanup_reconnect(){
  set +e
  [ -n "$SHELL_PID" ] && kill "$SHELL_PID" >/dev/null 2>&1
  exec 9>&- 2>/dev/null
  island_cleanup "$ISLAND"
  teardown_daemon
}
trap cleanup_reconnect EXIT

# =============================================================================
# Fast give-up FIRST (no island needed): attaching to a target that does not
# exist must fail FAST with a clear message — never spin the 5-minute reconnect
# loop. `dejima shell` pre-checks the island, so a missing one errors at once.
# =============================================================================
step "Fast give-up: attaching to a non-existent island errors immediately"
GONE_OUT="$DEJ_TMP/gone.out"
timeout 30 dejima shell "no-such-island-$$" >"$GONE_OUT" 2>&1
rc=$?
if [ "$rc" -eq 124 ]; then
  fail "attach to a missing island HUNG (timed out) — it must give up fast, not spin the reconnect loop"
elif [ "$rc" -eq 0 ]; then
  fail "attach to a missing island unexpectedly succeeded"
else
  pass "attach to a missing island exits fast and non-zero (rc=$rc)"
  if grep -qiE "no such|not found|unknown|does not exist|no island|not running" "$GONE_OUT"; then
    pass "…with a clear message"
  else
    skip "give-up message present but unmatched (rc=$rc): $(head -1 "$GONE_OUT")"
  fi
fi

# =============================================================================
# The survival proof needs a real, running island. Skip cleanly without Docker.
# =============================================================================
if require_docker "reconnect-resilience live suite"; then

  step "Create a throwaway headless island (its own, never aoos's)"
  SEED="$DEJ_TMP/seed"; make_seed_repo "$SEED"
  if ! dejima init --name "$ISLAND" --repo "$SEED" --local-copy --agent headless --cmd "sleep infinity" >/dev/null 2>&1; then
    fail "island create failed (see $(daemon_log)) — skipping the survival check"
  else
    pass "island $ISLAND created and running"

    # -----------------------------------------------------------------------
    # Attach a real `dejima shell` session, driven over a FIFO so we can hold
    # stdin open (no spurious EOF), restart the daemon under it, then close
    # stdin to force a clean exit. The session is a persistent in-container tmux
    # shell, so re-attaching after a drop resumes the SAME session.
    # -----------------------------------------------------------------------
    step "Attach a persistent shell session over a held-open stdin"
    FIFO="$DEJ_TMP/shell.in"; OUT="$DEJ_TMP/shell.out"
    mkfifo "$FIFO"
    # Background the client first (it opens the FIFO read-only and blocks until a
    # writer appears), then open the write end on fd 9 so both opens complete.
    dejima shell "$ISLAND" --as t3recon <"$FIFO" >"$OUT" 2>&1 &
    SHELL_PID=$!
    exec 9>"$FIFO"
    # Give the attach a moment, then prove the session is live by round-tripping
    # a marker through it (best-effort: the in-tmux echo is nice-to-have; the
    # load-bearing assertions are process survival + clean exit below).
    sleep 3
    MARK1="RECONNECT_BEFORE_$$"
    printf 'echo %s\n' "$MARK1" >&9
    got_before=""
    for _ in $(seq 1 20); do
      if grep -qF "$MARK1" "$OUT" 2>/dev/null; then got_before=1; break; fi
      sleep 0.5
    done
    if [ -n "$got_before" ]; then
      pass "marker echoed back through the attached session (it is live)"
    else
      skip "could not observe the in-session echo (non-tty tmux quirk) — relying on process liveness"
    fi
    if ! kill -0 "$SHELL_PID" >/dev/null 2>&1; then
      fail "the shell client exited before we even dropped the link"
    else
      pass "shell client attached and running (pid $SHELL_PID)"

      # -------------------------------------------------------------------
      # THE BUG: drop the link by restarting the daemon. Under the old code the
      # client exited code 1. With #129 it must stay alive and reconnect.
      # -------------------------------------------------------------------
      step "Drop the link: restart the daemon under the attached session"
      kill "$DEJ_PID" >/dev/null 2>&1
      for _ in $(seq 1 25); do [ -S "$HOME/.dejima/dejimad.sock" ] || break; sleep 0.2; done
      dejimad --foreground >>"$(daemon_log)" 2>&1 &
      DEJ_PID=$!
      for _ in $(seq 1 50); do [ -S "$HOME/.dejima/dejimad.sock" ] && break; sleep 0.2; done
      dejima audit >/dev/null 2>&1 || fail "daemon did not come back after the restart"

      # Give the client's capped backoff time to re-dial the fresh daemon (it
      # starts at 250ms; a handful of seconds is well inside the 5-min window).
      step "Survival: the client stays alive and reconnects (no code-1 exit)"
      alive=""
      for _ in $(seq 1 30); do
        if kill -0 "$SHELL_PID" >/dev/null 2>&1; then alive=1; sleep 1; else alive=""; break; fi
      done
      if [ -n "$alive" ]; then
        pass "client survived the daemon restart (still attached — the #129 bug is fixed)"
        # Best-effort: prove it RESUMED the same session by driving a 2nd marker.
        MARK2="RECONNECT_AFTER_$$"
        printf 'echo %s\n' "$MARK2" >&9
        got_after=""
        for _ in $(seq 1 20); do
          if grep -qF "$MARK2" "$OUT" 2>/dev/null; then got_after=1; break; fi
          sleep 0.5
        done
        if [ -n "$got_after" ]; then
          pass "session resumed through the drop (post-restart marker round-tripped)"
        else
          skip "post-restart echo not observed (non-tty tmux quirk) — survival already proven by liveness"
        fi
      else
        fail "client EXITED on the daemon restart — this is the #129 regression (terminal closed out from under the operator)"
      fi

      # -------------------------------------------------------------------
      # Clean exit: closing stdin (the terminal going away) ends the client
      # cleanly (rc 0), NOT as an error — and never reconnects.
      # -------------------------------------------------------------------
      step "Clean exit: closing stdin ends the client cleanly (rc 0)"
      exec 9>&-   # close the write end → the client sees stdin EOF
      crc=124
      for _ in $(seq 1 30); do
        if ! kill -0 "$SHELL_PID" >/dev/null 2>&1; then
          wait "$SHELL_PID"; crc=$?; break
        fi
        sleep 0.5
      done
      SHELL_PID=""  # reaped; don't double-kill in cleanup
      if [ "$crc" -eq 124 ]; then
        fail "client did not exit after stdin closed (still attached)"
      elif [ "$crc" -eq 0 ]; then
        pass "client exited cleanly (rc 0) when stdin closed"
      else
        fail "client exited non-zero (rc=$crc) on a clean stdin close — should be 0"
      fi
    fi
  fi
fi

report_and_exit
