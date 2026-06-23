# shellcheck shell=bash
# Tier-3 macOS-host test suite — shared helpers.
#
# Sourced by every scripts/tier3/*.sh test. Provides the pass/fail tally, an
# isolated dejimad (its OWN $HOME / colima, never the operator's aoos daemon),
# and idempotent teardown. Run as the `dejimaqa` test user on the Mac-mini
# self-hosted runner. See docs/testing/dejimaqa-runner-setup.md.
#
# Isolation principle: these tests must NEVER touch a real aoos island, the aoos
# ~/.dejima, or the aoos colima VM. Each "safe" test stands up a throwaway
# dejimad under a temp $HOME and purges everything on exit. Only the explicitly
# system-mutating tests (behind run_system_tests) use scoped sudo, and they
# install->verify->uninstall.

# ---------------------------------------------------------------------------
# Tally + reporting (mirrors scripts/integration.sh conventions).
# ---------------------------------------------------------------------------
PASS=0 FAIL=0 SKIP=0
pass(){ printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }
skip(){ printf '  \033[33m∼\033[0m %s (skipped)\n' "$*"; SKIP=$((SKIP+1)); }
step(){ printf '\n\033[1m• %s\033[0m\n' "$*"; }
die(){ printf '\033[31mFATAL: %s\033[0m\n' "$*" >&2; exit 1; }

assert_eq(){ if [ "$1" = "$2" ]; then pass "$3"; else fail "$3 — got [$1] want [$2]"; fi; }
assert_has(){ if printf '%s' "$1" | grep -qF -- "$2"; then pass "$3"; else fail "$3 — missing [$2]"; fi; }
expect_ok(){ local d="$1"; shift; if "$@" >/dev/null 2>&1; then pass "$d"; else fail "$d — command failed: $*"; fi; }
expect_fail(){ local d="$1"; shift; if "$@" >/dev/null 2>&1; then fail "$d — expected failure but it succeeded"; else pass "$d"; fi; }

# report_and_exit: print the tally and exit non-zero on any failure. Skips don't
# fail the job (a missing prereq is a notice, not a regression).
report_and_exit(){
  printf '\n\033[1m──────── %s ────────\033[0m\n' "${SUITE_NAME:-tier3}"
  printf 'passed: %d   failed: %d   skipped: %d\n' "$PASS" "$FAIL" "$SKIP"
  [ "$FAIL" -eq 0 ] || { printf '\033[31m%s FAILED\033[0m\n' "${SUITE_NAME:-tier3}"; exit 1; }
  printf '\033[32m%s GREEN\033[0m\n' "${SUITE_NAME:-tier3}"
}

# ---------------------------------------------------------------------------
# Guards
# ---------------------------------------------------------------------------

# require_macos: most tier-3 checks are macOS-host behavior (launchd, Keychain,
# pmset). On a non-macOS runner they skip cleanly rather than fail.
require_macos(){
  if [ "$(uname -s)" != "Darwin" ]; then
    skip "${1:-this check} — not macOS (uname=$(uname -s))"
    return 1
  fi
  return 0
}

# refuse_as_aoos: hard stop if we're somehow running as the operator account.
# These tests stand up their own daemon and must never run as aoos.
refuse_as_aoos(){
  if [ "$(id -un)" = "aoos" ]; then
    die "tier-3 tests must run as the dejimaqa test user, not aoos"
  fi
}

# ---------------------------------------------------------------------------
# Isolated dejimad (safe tests). Builds the binaries into a temp dir, points
# $HOME at a throwaway dir, and starts a foreground daemon on that HOME's
# socket — completely separate from any aoos dejimad. Sets:
#   DEJ_BIN   — dir holding the freshly built dejima/dejimad
#   DEJ_HOME  — the throwaway $HOME (also exported as HOME)
#   DEJ_PID   — the daemon pid
# Honors DEJIMAD_IDLE_HIBERNATE (callers may export it before calling).
# ---------------------------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEJ_TMP="" DEJ_BIN="" DEJ_HOME="" DEJ_PID="" REAL_HOME="$HOME"

build_dejima(){
  step "Build dejima + dejimad (isolated)"
  DEJ_TMP="$(mktemp -d)"
  DEJ_BIN="$DEJ_TMP/bin"; mkdir -p "$DEJ_BIN"
  ( cd "$REPO_ROOT" && go build -o "$DEJ_BIN/dejima" ./cmd/dejima && go build -o "$DEJ_BIN/dejimad" ./cmd/dejimad ) \
    || die "build failed"
  export PATH="$DEJ_BIN:$PATH"
  pass "binaries built into $DEJ_BIN"
}

start_daemon(){
  step "Start an isolated dejimad (own HOME, never aoos's)"
  DEJ_HOME="$DEJ_TMP/home"; mkdir -p "$DEJ_HOME"
  export HOME="$DEJ_HOME"
  # Docker CLI config lives in the real ~/.docker; point back so colima/Docker
  # resolves (same reasoning as scripts/integration.sh).
  export DOCKER_CONFIG="${DOCKER_CONFIG:-$REAL_HOME/.docker}"
  dejimad --foreground >"$DEJ_TMP/dejimad.log" 2>&1 &
  DEJ_PID=$!
  local _i
  for _i in $(seq 1 50); do [ -S "$HOME/.dejima/dejimad.sock" ] && break; sleep 0.2; done
  [ -S "$HOME/.dejima/dejimad.sock" ] || die "daemon socket never appeared (see $DEJ_TMP/dejimad.log)"
  dejima audit >/dev/null 2>&1 || die "daemon not responding"
  pass "isolated daemon up (pid $DEJ_PID, HOME=$HOME)"
}

daemon_log(){ printf '%s\n' "$DEJ_TMP/dejimad.log"; }

# island_cleanup <name...> — purge throwaway islands quietly.
island_cleanup(){
  local n
  for n in "$@"; do dejima purge "$n" -f >/dev/null 2>&1 || true; done
}

# stop_daemon + full temp teardown. Registered via trap by tests that build.
teardown_daemon(){
  set +e
  [ -n "$DEJ_PID" ] && kill "$DEJ_PID" >/dev/null 2>&1
  [ -n "$DEJ_TMP" ] && { chmod -R u+w "$DEJ_TMP" 2>/dev/null; rm -rf "$DEJ_TMP"; }
}

# make_seed_repo <dir> — a tiny local git repo to seed a headless island from.
make_seed_repo(){
  local d="$1"
  mkdir -p "$d"
  ( cd "$d" && git init -q && git config user.email t@t && git config user.name t \
    && echo "# tier3 seed" > README.md && git add -A && git commit -qm init ) \
    || die "seed repo setup failed"
}

# require_docker: island-backed safe tests need colima/Docker. Skip (not fail)
# when the daemon isn't reachable, so a runner without colima up still passes.
require_docker(){
  if ! command -v docker >/dev/null 2>&1; then
    skip "${1:-this check} — docker not found"
    return 1
  fi
  if ! docker info >/dev/null 2>&1; then
    skip "${1:-this check} — docker daemon not reachable (colima not started?)"
    return 1
  fi
  return 0
}
