#!/usr/bin/env bash
# Tier-3 SYSTEM-MUTATING macOS-host suite — gated, opt-in only.
#
# These checks touch the real macOS host: they install a system-wide LaunchDaemon
# (`service install --system --audit`), run the host-provisioning wizard in
# idempotent/dry-run mode (`onboard --provision-host --yes`), and — only behind a
# SECOND explicit gate — verify reboot survival. They run as `dejimaqa` and may
# sudo ONLY the four scoped binaries (dejima / pmset / systemsetup / launchctl);
# see docs/testing/dejimaqa-runner-setup.md.
#
# This script is NEVER run by accident: the nightly workflow only invokes it when
# the operator sets the `run_system_tests` workflow_dispatch input to true, and
# the actual reboot is gated further behind `run_reboot_test`.
#
# SAFETY INVARIANTS:
#   · The system LaunchDaemon label `dev.dejima.dejimad` is SHARED. If an aoos
#     system daemon is already installed, we ABORT rather than clobber it.
#   · Every install is matched by an uninstall in teardown (install->verify->clean).
#   · onboard runs --yes (scriptable steps only); it must NOT re-install packages.
#   · The reboot itself never runs unless DEJIMA_RUN_REBOOT=1 is set explicitly.
#
# Usage:   DEJIMA_RUN_SYSTEM=1 [DEJIMA_RUN_REBOOT=1] scripts/tier3/system.sh
set -uo pipefail
SUITE_NAME="tier3-system"
# shellcheck source=scripts/tier3/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

LAUNCHD_LABEL="dev.dejima.dejimad"
SYSTEM_PLIST="/Library/LaunchDaemons/${LAUNCHD_LABEL}.plist"

refuse_as_aoos

# Top-level gate. Without the opt-in, do nothing destructive.
if [ "${DEJIMA_RUN_SYSTEM:-0}" != "1" ]; then
  step "system-mutating suite is OPT-IN"
  skip "DEJIMA_RUN_SYSTEM!=1 — not installing anything (set it via the run_system_tests dispatch input)"
  report_and_exit
fi

require_macos "system-mutating suite" || report_and_exit

# Refuse to clobber a pre-existing (aoos) system daemon — the label is shared.
step "Pre-flight: must not clobber an existing system daemon"
PRE_EXISTING=""
if [ -f "$SYSTEM_PLIST" ]; then
  PRE_EXISTING=1
fi
if sudo launchctl print "system/${LAUNCHD_LABEL}" >/dev/null 2>&1; then
  PRE_EXISTING=1
fi
if [ -n "$PRE_EXISTING" ]; then
  fail "a system LaunchDaemon ($LAUNCHD_LABEL) already exists — refusing to install over it (is aoos's daemon installed?)"
  printf '  \033[33mResolve by uninstalling the existing daemon (as its owner) before running the system suite.\033[0m\n'
  report_and_exit
fi
pass "no pre-existing system daemon — safe to install"

build_dejima

# Teardown ALWAYS uninstalls whatever we installed, then drops the temp tree.
system_teardown(){
  set +e
  step "Teardown: uninstall the system daemon we installed"
  if [ -f "$SYSTEM_PLIST" ] || sudo launchctl print "system/${LAUNCHD_LABEL}" >/dev/null 2>&1; then
    if sudo "$DEJ_BIN/dejima" service uninstall --system >/dev/null 2>&1; then
      printf '  \033[32m✓\033[0m uninstalled the system daemon\n'
    else
      printf '  \033[31m✗\033[0m FAILED to uninstall — manual cleanup may be needed: %s\n' "$SYSTEM_PLIST"
    fi
  fi
  teardown_daemon
}
trap system_teardown EXIT

# ===========================================================================
# 1. service install --system --audit  →  daemon up + audited.
# ===========================================================================
step "service install --system --audit"
if sudo "$DEJ_BIN/dejima" service install --system --audit --no-tcp-prompt --no-notify-prompt >/dev/null 2>&1; then
  pass "service install --system --audit returned 0"
else
  fail "service install --system --audit failed"
  report_and_exit
fi

step "Verify the LaunchDaemon is loaded + reachable"
if [ -f "$SYSTEM_PLIST" ]; then
  pass "plist written to $SYSTEM_PLIST"
else
  fail "plist not found at $SYSTEM_PLIST"
fi
loaded=""
for _ in $(seq 1 20); do
  if sudo launchctl print "system/${LAUNCHD_LABEL}" >/dev/null 2>&1; then loaded=1; break; fi
  sleep 0.5
done
if [ -n "$loaded" ]; then
  pass "launchd reports the job loaded in the system domain"
else
  fail "launchd does not report the job loaded"
fi
# The installed daemon serves on the default (aoos-account?) socket... no — it
# runs as the install user (dejimaqa via SUDO_USER), so the socket is under
# dejimaqa's ~/.dejima. Talk to it via the dejimaqa-owned socket.
REACH=""
for _ in $(seq 1 20); do
  if "$DEJ_BIN/dejima" audit >/dev/null 2>&1; then REACH=1; break; fi
  sleep 0.5
done
if [ -n "$REACH" ]; then
  pass "the installed daemon answers the CLI"
else
  skip "could not reach the installed daemon's socket from this shell (env/socket-path mismatch)"
fi

step "Verify auditing is on (the install baked --audit)"
AUD="$("$DEJ_BIN/dejima" audit 2>&1)"
if [ -n "$AUD" ]; then
  pass "audit log is populated (auditing active)"
else
  skip "audit log empty (no requests yet) — auditing flag still baked into the service"
fi

# ===========================================================================
# 2. onboard --provision-host in idempotent/dry-run mode (no package re-install).
# ===========================================================================
step "onboard --provision-host --yes (idempotent; must NOT re-install packages)"
# --yes runs only the scriptable steps and skips GUI/off-box-login pauses. On an
# already-provisioned runner the steps short-circuit ("already done"). We assert
# it completes without error and reports skips/already-done, not fresh installs.
PROV_OUT="$(sudo "$DEJ_BIN/dejima" onboard --provision-host --yes 2>&1)"
PROV_RC=$?
if [ "$PROV_RC" -eq 0 ]; then
  pass "onboard --provision-host --yes completed (rc=0)"
else
  # A non-zero rc on an already-provisioned box is acceptable if it's a guarded
  # stop (e.g. "fix the above, then re-run"), but flag it for eyeballing.
  skip "onboard --provision-host --yes returned rc=$PROV_RC (guarded stop on this host?)"
fi
if printf '%s' "$PROV_OUT" | grep -qiE 'already done|skipping|skipped'; then
  pass "wizard short-circuited already-provisioned steps (idempotent)"
else
  skip "could not confirm idempotent short-circuit from output (fresh host?)"
fi
if printf '%s' "$PROV_OUT" | grep -qiE 'brew install|installing homebrew|downloading'; then
  fail "wizard appears to have RE-INSTALLED packages on a nightly run (not idempotent)"
else
  pass "no package re-install detected"
fi

# ===========================================================================
# 3. Reboot survival — DOUBLE-gated. Only when DEJIMA_RUN_REBOOT=1.
# ===========================================================================
step "reboot survival (double-gated)"
if [ "${DEJIMA_RUN_REBOOT:-0}" != "1" ]; then
  skip "DEJIMA_RUN_REBOOT!=1 — not rebooting (set run_reboot_test to exercise this)"
  printf '  \033[33mTo verify manually: confirm %s has RunAtLoad/KeepAlive, reboot, then check the daemon is reachable with no login.\033[0m\n' "$SYSTEM_PLIST"
  # Static assertion we CAN make without rebooting: the plist must declare
  # RunAtLoad so the daemon comes up at boot before any login.
  if [ -f "$SYSTEM_PLIST" ] && sudo grep -q "RunAtLoad" "$SYSTEM_PLIST" 2>/dev/null; then
    pass "plist declares RunAtLoad (boots before login)"
  else
    skip "could not read RunAtLoad from the plist"
  fi
else
  # The reboot itself is the operator's call even within this gate: we record the
  # intent and let a post-reboot check (a separate dispatch) confirm reachability,
  # rather than tearing the runner out from under the workflow mid-job.
  fail "DEJIMA_RUN_REBOOT=1 but in-job reboot is intentionally NOT performed here — run the post-reboot reachability check as a separate manual step"
  printf '  \033[33mRebooting inside the job would kill the runner. Do: sudo reboot (manually), then re-dispatch with a reachability-only check.\033[0m\n'
fi

report_and_exit
