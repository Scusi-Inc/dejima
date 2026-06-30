# shellcheck shell=bash
# Clean-Mac verify harness — shared helpers + per-channel install/uninstall.
#
# Sourced by teardown.sh, provision.sh and proof-loop.sh. This is the LAUNCH-GATE
# harness (Lane 0): it proves install + uninstall + re-adopt on a REAL clean Mac,
# the single biggest remaining launch risk. It ASSEMBLES the existing in-island
# checks — it does not re-implement them:
#   · scripts/install-channels-check.sh — the 21-assertion channel-consistency gate
#   · scripts/integration.sh's "uninstall --keep-islands + re-adopt" feature — the
#     Docker round-trip that proves volumes + ~/.dejima survive an uninstall
#
# Isolation principle (same as scripts/tier3/lib.sh): on the Minion this runs as
# the `dejimaqa` test user against ITS OWN colima Docker + its own ~/.dejima, and
# REFUSES to run as `aoos` — the teardown deletes ~/.dejima and ~/.dejima-src, so
# pointing it at the operator account would destroy real islands. The channels
# install the operator-facing `dejima`/`dejimad` to the user PREFIX (default
# ~/.local), never /usr/local, so no admin/sudo is needed and the operator's
# /usr/local/bin/dejima* (if any) is left untouched.

set -uo pipefail

# ---------------------------------------------------------------------------
# Tally + reporting (mirrors scripts/tier3/lib.sh / integration.sh).
# ---------------------------------------------------------------------------
PASS=0 FAIL=0 SKIP=0
pass(){ printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip(){ printf '  \033[33m∼\033[0m %s (skipped)\n' "$*"; SKIP=$((SKIP+1)); }
step(){ printf '\n\033[1m• %s\033[0m\n' "$*"; }
hdr(){  printf '\n\033[1;36m══════ %s ══════\033[0m\n' "$*"; }
die(){  printf '\033[31mFATAL: %s\033[0m\n' "$*" >&2; exit 1; }

assert_eq(){ if [ "$1" = "$2" ]; then pass "$3"; else fail "$3 — got [$1] want [$2]"; fi; }
# expect_ok / expect_fail: run a command, assert exit status, never abort.
expect_ok(){   local d="$1"; shift; if "$@" >/dev/null 2>&1; then pass "$d"; else fail "$d — command failed: $*"; fi; }
expect_fail(){ local d="$1"; shift; if "$@" >/dev/null 2>&1; then fail "$d — expected failure but it succeeded"; else pass "$d"; fi; }

report_and_exit(){
  printf '\n\033[1m──────── %s ────────\033[0m\n' "${SUITE_NAME:-clean-mac}"
  printf 'passed: %d   failed: %d   skipped: %d\n' "$PASS" "$FAIL" "$SKIP"
  [ "$FAIL" -eq 0 ] || { printf '\033[31m%s FAILED\033[0m\n' "${SUITE_NAME:-clean-mac}"; exit 1; }
  printf '\033[32m%s GREEN\033[0m\n' "${SUITE_NAME:-clean-mac}"
}

# ---------------------------------------------------------------------------
# Paths + knobs
# ---------------------------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# User-prefix install: binaries land in $PREFIX/bin, never /usr/local. Keeps the
# whole harness sudo-free and isolated from any operator install.
PREFIX="${DEJIMA_PREFIX:-$HOME/.local}"
BIN_DIR="$PREFIX/bin"
DEJIMA_SRC="${DEJIMA_SRC_DIR:-$HOME/.dejima-src}"
DEJIMA_HOME="$HOME/.dejima"
# REAL_HOME is the operator's true home — consumed by the sourcing scripts
# (proof-loop.sh: seed-repo dir + the dejimad foreground-log fallback). Defined
# here next to DEJIMA_HOME because lib.sh runs under `set -u`: without it those
# scripts abort with "REAL_HOME: unbound variable" before any assertion (every
# full-host channel). Mirrors integration.sh / tier3/lib.sh. Consumed downstream,
# so shellcheck (linting this file standalone) can't see the use.
# shellcheck disable=SC2034
REAL_HOME="$HOME"
# Deterministic test-island names + the workspace marker that must survive a
# --keep-islands uninstall→reinstall round-trip. MARKER_TEXT/CFG are consumed by
# the sourcing scripts (proof-loop.sh), not here — so shellcheck, which lints this
# file standalone, can't see the use. Same convention as scripts/lib/report.sh.
# shellcheck disable=SC2034
ISLAND="${CLEANMAC_ISLAND:-cleanmac-readopt}"
# shellcheck disable=SC2034
MARKER_TEXT="readopt-survives"
# shellcheck disable=SC2034
WS_VOL="dejima-$ISLAND-workspace"
# shellcheck disable=SC2034
CFG="$DEJIMA_HOME/projects/$ISLAND/config.toml"

# ---------------------------------------------------------------------------
# Guards
# ---------------------------------------------------------------------------

# refuse_as_aoos: the teardown DESTROYS ~/.dejima + ~/.dejima-src. Running as the
# operator account would wipe real islands. Hard-stop unless explicitly overridden
# for a knowing operator on a throwaway box (CLEANMAC_ALLOW_AOOS=1).
refuse_as_aoos(){
  if [ "$(id -un)" = "aoos" ] && [ "${CLEANMAC_ALLOW_AOOS:-}" != "1" ]; then
    die "clean-mac harness must run as the dejimaqa test user, not aoos (it deletes ~/.dejima + ~/.dejima-src). Set CLEANMAC_ALLOW_AOOS=1 only on a throwaway box."
  fi
}

# refuse_if_live_daemon: the gate's teardown runs `dejima uninstall --purge-all`
# and the install channels bind the operator daemon ports (:7273/:7274). Run
# co-resident with a LIVE daemon it takes that daemon OFFLINE — it happened on
# Minion (2026-06-29: the operator daemon went down, recovered, no data lost).
# refuse_as_aoos guards the *user*, but a system LaunchDaemon (installed with
# `dejima service install --system`) runs regardless of which user invokes the
# gate, so the user check alone isn't enough. Hard-stop on ANY sign of a live or
# system daemon — a loaded LaunchDaemon, a bound daemon port, or a dejimad owned
# by another user. Override only on a genuinely isolated throwaway box with
# CLEANMAC_ALLOW_LIVE_DAEMON=1.
refuse_if_live_daemon(){
  [ "${CLEANMAC_ALLOW_LIVE_DAEMON:-}" = "1" ] && return 0
  local found=""
  # 1. A loaded system LaunchDaemon (the operator's boot-persistent daemon).
  if have launchctl; then
    if launchctl print "system/${DEJIMA_LAUNCHD_LABEL:-dev.dejima.dejimad}" >/dev/null 2>&1; then
      found="a loaded system LaunchDaemon (dev.dejima.dejimad)"
    fi
  fi
  # 2. The operator daemon ports are bound (API + TCP listener).
  if have lsof; then
    for port in 7273 7274; do
      if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
        found="${found:+$found; }a process listening on :$port"
      fi
    done
  fi
  # 3. A dejimad owned by ANOTHER user — i.e. a host daemon, not the throwaway one
  #    teardown would manage. (Our own stray foreground dejimad is fine; teardown
  #    reaps it.)
  if pgrep -x dejimad >/dev/null 2>&1 && ! pgrep -u "$(id -u)" -x dejimad >/dev/null 2>&1; then
    found="${found:+$found; }a dejimad owned by another user"
  fi
  if [ -n "$found" ]; then
    die "clean-mac harness REFUSES to run — detected: ${found}. Teardown purges islands and binds :7273/:7274, which would take a LIVE daemon offline. Run ONLY on a throwaway box with NO Dejima daemon. If this box is genuinely isolated and you know what you're doing: CLEANMAC_ALLOW_LIVE_DAEMON=1."
  fi
}

require_macos_or_note(){
  if [ "$(uname -s)" != "Darwin" ]; then
    printf '\033[33mNOTE: not macOS (uname=%s) — the live launch gate is the clean-Mac run. Channels that need brew/colima will skip.\033[0m\n' "$(uname -s)" >&2
  fi
}

have(){ command -v "$1" >/dev/null 2>&1; }

require_docker(){
  if ! have docker; then skip "${1:-this step} — docker not found"; return 1; fi
  if ! docker info >/dev/null 2>&1; then skip "${1:-this step} — docker daemon not reachable (colima not started?)"; return 1; fi
  return 0
}

# ---------------------------------------------------------------------------
# Virgin-environment teardown. Idempotent: safe to run on an already-clean box.
# Removes every trace of a prior Dejima install so each channel starts virgin —
# the whole point of the launch gate. Scoped to the CURRENT user's $HOME +
# user-prefix binaries; it does NOT touch /usr/local, system launchd, or colima
# state (colima reset is a separate, explicit step — see reset_colima).
# ---------------------------------------------------------------------------
teardown_dejima(){
  step "Teardown: remove all Dejima state for $(id -un) (idempotent)"

  # 1. If a daemon/binary is present, ask it to uninstall cleanly first (best
  #    effort): stops containers, removes the service + binaries, PURGES all
  #    islands (this is the full reset, not --keep-islands). Never fatal.
  if have dejima; then
    dejima uninstall --purge-all --yes >/dev/null 2>&1 || true
  fi

  # 1b. Kill any hand-started foreground dejimad. `uninstall` stops the SERVICE,
  #     not a `dejimad --foreground &` (the proof loop's fallback when the service
  #     path isn't up). Without this a stray daemon survives into the next channel
  #     and wait_for_daemon passes against a STALE socket, masking an install
  #     failure. Scoped to THIS user's processes (teardown refuses to run as the
  #     operator), so it never touches a real host daemon.
  pkill -u "$(id -u)" -x dejimad >/dev/null 2>&1 || true

  # 2. Package-manager channels, removed if present (best effort, no sudo).
  if have brew; then
    brew uninstall dejima >/dev/null 2>&1 || true
    brew untap aoos/dejima >/dev/null 2>&1 || true
  fi
  if have npm; then
    npm uninstall -g dejima >/dev/null 2>&1 || true
  fi

  # 3. Backstop: nuke any island docker objects matching our test name directly,
  #    plus the source checkout + config + user-prefix binaries, in case the
  #    uninstall above didn't run (no binary present) or left a stray.
  if have docker && docker info >/dev/null 2>&1; then
    docker rm -f "dejima-$ISLAND" >/dev/null 2>&1 || true
    docker volume rm -f "dejima-$ISLAND-workspace" "dejima-$ISLAND-home" >/dev/null 2>&1 || true
    docker network rm "dejima-net-$ISLAND" >/dev/null 2>&1 || true
  fi
  rm -rf "$DEJIMA_HOME" "$DEJIMA_SRC" 2>/dev/null || true
  rm -f "$BIN_DIR/dejima" "$BIN_DIR/dejimad" 2>/dev/null || true

  pass "Dejima state removed (~/.dejima, ~/.dejima-src, $BIN_DIR/dejima*, test island docker objects)"
}

# assert_virgin: confirm the box really is clean before an install — the gate is
# only meaningful starting from a virgin env. Reports, never aborts (the proof
# loop turns a non-virgin start into a failed run).
assert_virgin(){
  step "Assert virgin environment (no Dejima history)"
  # Check the USER PREFIX only, not `command -v` across all of PATH: the channels
  # install to $BIN_DIR ($PREFIX/bin, PATH-prepended), so that's the binary under
  # test. A pre-existing /usr/local/bin/dejima (another account's system install on
  # shared /usr/local) is irrelevant to a user-prefix gate — failing on it would
  # mean the gate could NEVER pass on any box that has a system dejima.
  if [ -e "$BIN_DIR/dejima" ]; then
    fail "a 'dejima' binary is still at $BIN_DIR/dejima — teardown incomplete"
  else
    pass "no dejima binary at $BIN_DIR (user prefix)"
  fi
  # A system dejima elsewhere on PATH is noted, not failed — it's not ours to remove.
  if have dejima && [ "$(command -v dejima)" != "$BIN_DIR/dejima" ]; then
    pass "(note) a system dejima exists at $(command -v dejima) — outside the gate's prefix, left untouched"
  fi
  if [ -e "$DEJIMA_HOME" ]; then fail "$DEJIMA_HOME still exists — teardown incomplete"; else pass "$DEJIMA_HOME absent"; fi
  if [ -e "$DEJIMA_SRC" ]; then fail "$DEJIMA_SRC still exists — teardown incomplete"; else pass "$DEJIMA_SRC absent"; fi
  if have docker && docker info >/dev/null 2>&1; then
    if docker volume inspect "$WS_VOL" >/dev/null 2>&1; then
      fail "$WS_VOL still exists — teardown incomplete"
    else
      pass "test island volume absent"
    fi
  fi
}

# reset_colima: the heavy virgin reset — a fresh colima VM (no prior Docker
# images/volumes at all). DESTRUCTIVE to every container on this user's colima,
# so it's opt-in (CLEANMAC_RESET_COLIMA=1) and only ever the dejimaqa VM. Most
# runs don't need it (teardown_dejima already removes Dejima's own objects); it's
# here for the once-in-a-while "truly from zero" full reset the brief asks for.
reset_colima(){
  if [ "${CLEANMAC_RESET_COLIMA:-}" != "1" ]; then
    skip "colima full reset (set CLEANMAC_RESET_COLIMA=1 for a from-zero Docker VM)"
    return 0
  fi
  if ! have colima; then skip "colima reset — colima not installed"; return 0; fi
  step "Reset colima VM (from-zero Docker; dejimaqa VM only)"
  colima delete --force >/dev/null 2>&1 || true
  colima start --cpu "${CLEANMAC_COLIMA_CPU:-4}" --memory "${CLEANMAC_COLIMA_MEM:-8}" --disk "${CLEANMAC_COLIMA_DISK:-60}" \
    >/dev/null 2>&1 || die "colima start failed after reset"
  docker info >/dev/null 2>&1 || die "docker unreachable after colima reset"
  pass "fresh colima VM up"
}

# ---------------------------------------------------------------------------
# Per-channel install. Each leaves a working `dejima` (and for full-host channels
# a `dejimad` + island image) on PATH, installed under $PREFIX so no sudo and no
# clobbering an operator install. Returns 0 on success, non-zero on failure.
#
# Channel realities (see docs/distribution.md + PR #86):
#   · curl|sh  → install.sh: full host — clones source, builds dejima + dejimad,
#                installs to $PREFIX/bin, builds the island image, registers the
#                daemon. The complete stack; the re-adopt round-trip runs here.
#   · brew     → the PINNED formula ships the CLI binary only. For a full-stack
#                gate we use `brew install --HEAD aoos/dejima/dejima`, which
#                builds BOTH dejima + dejimad from source off master (the formula
#                installs both from the Unix tarball / HEAD build). CLIENT-ONLY
#                pinned-binary install is checked separately as a smoke.
#   · npm      → CLIENT ONLY by design (the daemon is Unix-host-only). The npm
#                proof verifies the client installs + runs and can DRIVE a daemon
#                that a source install already stood up; it does not stand up its
#                own daemon. (npm channel = client distribution, not host.)
# ---------------------------------------------------------------------------

# install.sh / brew --HEAD honor PREFIX + AUTO_INSTALL_DOCKER. Export once.
export PREFIX
export PATH="$BIN_DIR:$PATH"

install_channel_curl(){
  step "Install via curl|sh (install.sh — full host, PREFIX=$PREFIX)"
  # Prefer the served one-liner; fall back to the in-repo install.sh when run on
  # a CI checkout (the operator runs the SERVED curl; CI runs the repo copy so the
  # gate tests the branch under review). DEJIMA_REF pins the ref being verified.
  local rc=0
  if [ "${CLEANMAC_USE_SERVED:-}" = "1" ]; then
    curl -fsSL "${CLEANMAC_INSTALL_URL:-https://dejima.tech/install.sh}" \
      | AUTO_INSTALL_DOCKER=1 PREFIX="$PREFIX" DEJIMA_REF="${DEJIMA_REF:-master}" bash || rc=$?
  else
    AUTO_INSTALL_DOCKER=1 PREFIX="$PREFIX" DEJIMA_REF="${DEJIMA_REF:-master}" \
      DEJIMA_SRC_DIR="$DEJIMA_SRC" bash "$REPO_ROOT/install.sh" || rc=$?
  fi
  return "$rc"
}

install_channel_brew(){
  step "Install via brew (--HEAD: builds dejima + dejimad from source)"
  if ! have brew; then skip "brew channel — Homebrew not installed"; return 2; fi
  # The pinned tap may not exist yet (needs aoos/homebrew-dejima); --HEAD builds
  # from the public git repo off master and installs BOTH binaries, which is the
  # full-host stack we need for the re-adopt round-trip. Tap the formula from this
  # repo so we test the formula under review, not a stale tap.
  brew tap-new aoos/dejima >/dev/null 2>&1 || true
  cp "$REPO_ROOT/homebrew/dejima.rb" "$(brew --repository)/Library/Taps/aoos/homebrew-dejima/Formula/dejima.rb" 2>/dev/null || true
  local rc=0
  brew install --HEAD aoos/dejima/dejima || rc=$?
  return "$rc"
}

# install_channel_brew_pinned: smoke the PINNED binary path (CLI only). Used to
# confirm the pinned formula resolves + installs the client; not the full-stack
# round-trip. Skips cleanly if no published release/tap backs the pin.
install_channel_brew_pinned(){
  step "Smoke: brew pinned binary (CLI client only)"
  if ! have brew; then skip "brew pinned — Homebrew not installed"; return 2; fi
  local rc=0
  brew install aoos/dejima/dejima || rc=$?
  return "$rc"
}

install_channel_npm(){
  step "Install via npm (-g dejima — CLI client only)"
  if ! have npm; then skip "npm channel — npm not installed"; return 2; fi
  local rc=0
  npm install -g dejima || rc=$?
  return "$rc"
}

# uninstall_keep_islands: the central guarantee under test — remove Dejima but
# KEEP island volumes + ~/.dejima config. Stops the daemon/containers, removes the
# binaries; the volume + config must remain on disk.
uninstall_keep_islands(){
  step "dejima uninstall --keep-islands (remove daemon + binaries, KEEP volumes + config)"
  dejima uninstall --keep-islands --yes >/dev/null 2>&1 || true
}
