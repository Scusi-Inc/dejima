#!/usr/bin/env bash
# Clean-Mac PROOF LOOP — the launch gate, fully scripted, operator-runnable.
#
# For EACH install channel (curl|sh, brew, npm) it runs the full round-trip on a
# real clean Mac:
#
#   teardown (virgin)  →  install  →  assert daemon up + a test island running
#       →  write a workspace marker  →  dejima uninstall --keep-islands
#       →  assert volume + ~/.dejima config SURVIVE  →  reinstall
#       →  assert the island + its marker RE-ADOPT (same name → same volume)
#
# This is the single biggest remaining launch risk (install/uninstall on a real
# clean Mac), proven REPEATABLY. It ASSEMBLES, it does not rebuild:
#   · scripts/install-channels-check.sh — the channel-consistency gate, run once
#     up front (no Docker / no Mac needed; catches a broken install path early).
#   · the round-trip assertions mirror scripts/integration.sh's "uninstall
#     --keep-islands + re-adopt" feature — the same volume-survives + marker-
#     survives + re-adopt checks, but across a REAL channel install/uninstall
#     instead of one in-process $HOME.
#
# HARD CONSTRAINT (Lane 0): the author CANNOT self-verify — the live run needs a
# clean macOS host (Minion) and is OPERATOR-GATED. This script is the thing the
# operator runs there; a green run here is the proof, not the author's say-so.
# See docs/operator-tests/clean-mac-launch-gate.md for the hand-off.
#
# Channel realities (see lib.sh + docs/distribution.md):
#   · curl|sh : full host — daemon + island image + binaries. Full round-trip.
#   · brew    : --HEAD builds dejima + dejimad from source (full host). Full
#               round-trip. The pinned-binary path is smoked separately (CLI only).
#   · npm     : CLIENT ONLY by design (daemon is Unix-host-only). The npm proof
#               verifies the client installs + can DRIVE a daemon stood up by a
#               source install; it does not run its own daemon round-trip.
#
# Usage:
#   scripts/clean-mac/proof-loop.sh [--channels curl,brew,npm] [--reset-colima]
#   CLEANMAC_USE_SERVED=1 …   curl the served dejima.tech/install.sh (operator
#                             default); unset = use the in-repo install.sh (CI,
#                             tests the branch under review).
# Requires: docker (colima up), go, git. brew/npm channels skip cleanly if absent.
# Refuses to run as `aoos` (teardown deletes ~/.dejima). Run as dejimaqa.
SUITE_NAME="clean-mac-launch-gate"
# shellcheck source=scripts/clean-mac/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

CHANNELS="curl,brew,npm"
for ((i=1; i<=$#; i++)); do
  arg="${!i}"
  case "$arg" in
    --channels) i=$((i+1)); CHANNELS="${!i}" ;;
    --channels=*) CHANNELS="${arg#*=}" ;;
    --reset-colima) export CLEANMAC_RESET_COLIMA=1 ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown arg: $arg" ;;
  esac
done

refuse_as_aoos
require_macos_or_note

# ---------------------------------------------------------------------------
# 0. Channel-consistency gate (reused, run once). No Docker / no Mac needed; a
#    broken install path is the #1 clean-Mac failure mode, so catch it first.
# ---------------------------------------------------------------------------
hdr "STEP 0 — install-channel consistency (reused: install-channels-check.sh)"
if bash "$REPO_ROOT/scripts/install-channels-check.sh"; then
  pass "install-channel consistency check (all channels internally coherent)"
else
  fail "install-channel consistency check FAILED — a channel is inconsistent; see output above"
fi

# wait_for_daemon: poll for the dejimad socket (~10s).
wait_for_daemon(){
  local i=0
  while [ "$i" -lt 50 ]; do
    [ -S "$DEJIMA_HOME/dejimad.sock" ] && return 0
    sleep 0.2; i=$((i+1))
  done
  [ -S "$DEJIMA_HOME/dejimad.sock" ]
}

# wait_island_exec: poll until the island is exec-able (container up against its
# volume), up to ~30s. Mirrors integration.sh's re-adopt wait.
wait_island_exec(){
  local i
  for i in $(seq 1 30); do
    dejima exec "$ISLAND" -- test -f /workspace/keep.txt >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

# make_seed_repo: a tiny local git repo to seed a headless island from (no GitHub
# needed — the gate must not depend on a network repo).
SEED_DIR=""
make_seed_repo(){
  SEED_DIR="$REAL_HOME/.cache/cleanmac-seed-$$"
  rm -rf "$SEED_DIR"; mkdir -p "$SEED_DIR"
  ( cd "$SEED_DIR" && git init -q && git config user.email t@t && git config user.name t \
    && echo "# clean-mac seed" > README.md && git add -A && git commit -qm init ) \
    || die "seed repo setup failed"
}

# ---------------------------------------------------------------------------
# run_full_roundtrip <channel-label> <install-fn>: the full launch-gate proof for
# a FULL-HOST channel (curl|sh, brew --HEAD). Reused per channel so the assertion
# set is identical — exactly the integration.sh re-adopt feature, over a real
# channel install/uninstall.
# ---------------------------------------------------------------------------
run_full_roundtrip(){
  local label="$1" install_fn="$2"
  hdr "CHANNEL: $label (full host round-trip)"

  step "Teardown → virgin"
  teardown_dejima
  assert_virgin

  step "Install ($label)"
  if "$install_fn"; then
    if have dejima && dejima --version >/dev/null 2>&1; then
      pass "$label install succeeded; dejima --version: $(dejima --version 2>&1 | head -1)"
    else
      fail "$label install ran but no working dejima on PATH"; return
    fi
  else
    local rc=$?
    if [ "$rc" -eq 2 ]; then skip "$label channel unavailable on this host"; return; fi
    fail "$label install FAILED (rc=$rc)"; return
  fi

  # The full-host channels register + start the daemon. Confirm it's reachable.
  if wait_for_daemon; then
    pass "daemon up after $label install (socket present)"
  else
    # Source/brew installs register a service; on a CI checkout it may need an
    # explicit foreground start. Try once, then assert.
    dejimad --foreground >"$REAL_HOME/cleanmac-$label-dejimad.log" 2>&1 &
    if wait_for_daemon; then
      pass "daemon up after explicit start ($label)"
    else
      fail "daemon socket never appeared after $label install"; return
    fi
  fi

  require_docker "$label island round-trip" || { skip "$label round-trip — no Docker"; return; }

  step "Create a test island + write a workspace marker"
  make_seed_repo
  if dejima init --name "$ISLAND" --repo "$SEED_DIR" --local-copy --agent headless --cmd "sleep infinity" >/dev/null 2>&1; then
    pass "test island '$ISLAND' created"
  else
    fail "test island create failed"; return
  fi
  if dejima exec "$ISLAND" -- sh -c "echo $MARKER_TEXT > /workspace/keep.txt" >/dev/null 2>&1; then
    pass "wrote workspace marker"
  else
    fail "could not write workspace marker"; return
  fi
  expect_ok "island is running (exec works)" dejima exec "$ISLAND" -- true
  expect_ok "workspace volume exists before uninstall" docker volume inspect "$WS_VOL"
  if [ -f "$CFG" ]; then pass "island config exists before uninstall"; else fail "island config missing before uninstall"; fi

  step "Bare uninstall REFUSES (no destructive default)"
  if dejima uninstall --yes >/dev/null 2>&1; then
    fail "bare 'uninstall --yes' did NOT refuse — it must demand an explicit choice"
  else
    pass "bare uninstall refused (explicit choice required)"
  fi
  expect_ok "volume untouched by the refusal" docker volume inspect "$WS_VOL"

  step "uninstall --keep-islands → KEEP volume + config"
  uninstall_keep_islands
  expect_ok "named volume SURVIVED --keep-islands" docker volume inspect "$WS_VOL"
  if [ -f "$CFG" ]; then pass "$CFG config SURVIVED --keep-islands"; else fail "config was deleted by --keep-islands (it must be kept)"; fi
  # The data is intact independent of any container/daemon.
  local voldata
  voldata="$(docker run --rm -v "$WS_VOL":/ws:ro dejima/island:latest sh -c 'cat /ws/keep.txt' 2>/dev/null)"
  assert_eq "$voldata" "$MARKER_TEXT" "workspace data persists in the kept volume (no daemon involved)"

  step "Reinstall ($label) → re-adopt the island + marker"
  if "$install_fn"; then
    pass "$label reinstall succeeded"
  else
    fail "$label reinstall FAILED"; return
  fi
  wait_for_daemon || { dejimad --foreground >>"$REAL_HOME/cleanmac-$label-dejimad.log" 2>&1 & wait_for_daemon; } \
    || { fail "daemon never came back after $label reinstall"; return; }

  # Kept config → the fresh daemon should already list the island. Config-less
  # reinstalls re-adopt on re-create (same name → same volume).
  if dejima ls 2>&1 | grep -qF -- "$ISLAND"; then
    pass "fresh daemon already lists the kept island (config re-adopted)"
  else
    dejima init --name "$ISLAND" --repo "$SEED_DIR" --local-copy --agent headless --cmd "sleep infinity" >/dev/null 2>&1 \
      || { fail "re-create after reinstall failed"; return; }
    pass "re-created island binds the same named volume"
  fi
  dejima wake "$ISLAND" >/dev/null 2>&1 || true
  if wait_island_exec; then
    local got
    got="$(dejima exec "$ISLAND" -- cat /workspace/keep.txt 2>/dev/null)"
    assert_eq "$got" "$MARKER_TEXT" "RE-ADOPTED: workspace survived the $label uninstall→reinstall round-trip"
  else
    fail "island never became exec-able after re-adoption"
  fi

  step "Cleanup ($label): purge the test island"
  dejima purge "$ISLAND" -f >/dev/null 2>&1 || true
  rm -rf "$SEED_DIR" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# run_npm_client: npm is a CLIENT-ONLY channel (no daemon). Prove the client
# installs + runs and can DRIVE a daemon that a source install already stood up.
# We stand up a source install first (so there IS a daemon), then swap in the npm
# client and confirm it talks to that daemon. No full re-adopt round-trip here —
# the daemon-side guarantee is already proven by the curl|sh channel.
# ---------------------------------------------------------------------------
run_npm_client(){
  hdr "CHANNEL: npm (client-only)"

  step "Teardown → virgin"
  teardown_dejima
  assert_virgin

  step "Stand up a daemon via curl|sh (source) so the npm client has a host to drive"
  if install_channel_curl && (wait_for_daemon || { dejimad --foreground >"$REAL_HOME/cleanmac-npm-dejimad.log" 2>&1 & wait_for_daemon; }); then
    pass "source daemon up for the npm client to drive"
  else
    fail "could not stand up a daemon for the npm client test"; return
  fi

  step "Install the npm client (-g dejima) over the top"
  # Remove the source CLI so the npm client is unambiguously the one on PATH.
  rm -f "$BIN_DIR/dejima" 2>/dev/null || true
  hash -r 2>/dev/null || true
  if install_channel_npm; then
    :
  else
    local rc=$?
    if [ "$rc" -eq 2 ]; then skip "npm channel unavailable on this host"; return; fi
    fail "npm install -g dejima FAILED (rc=$rc)"; return
  fi
  if have dejima && dejima --version >/dev/null 2>&1; then
    pass "npm client installed; dejima --version: $(dejima --version 2>&1 | head -1)"
  else
    fail "npm install ran but no working dejima client on PATH"; return
  fi

  step "npm client drives the running daemon"
  expect_ok "npm client lists islands against the live daemon" dejima ls
  if dejima --version 2>&1 | grep -qiE '[0-9]+\.[0-9]+'; then
    pass "npm client reports a version"
  else
    fail "npm client did not report a sane version"
  fi
  # The negative path (no prebuilt binary → friendly message) is already asserted
  # by install-channels-check.sh; nothing daemon-side to add here.

  step "Cleanup (npm)"
  dejima uninstall --purge-all --yes >/dev/null 2>&1 || true
  npm uninstall -g dejima >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
# Dispatch the requested channels.
# ---------------------------------------------------------------------------
IFS=',' read -ra CHANS <<< "$CHANNELS"
for ch in "${CHANS[@]}"; do
  case "$ch" in
    curl) run_full_roundtrip "curl|sh"      install_channel_curl ;;
    brew) run_full_roundtrip "brew --HEAD"  install_channel_brew
          if install_channel_brew_pinned; then
            pass "brew pinned-binary install resolved (CLI client)"
          else
            skip "brew pinned-binary install (no published release/tap backing the pin yet)"
          fi ;;
    npm)  run_npm_client ;;
    *) die "unknown channel: $ch (want: curl,brew,npm)" ;;
  esac
done

report_and_exit
