#!/usr/bin/env bash
# Dejima end-to-end integration test — runs against a LIVE Docker host.
#
# Verifies the full Port (brokered host-file access) plumbing plus create-time
# multi-agent seeding, against real containers:
#   · scope grant / deny-all
#   · intake (host -> island copy) incl. nested paths and a custom dest
#   · traversal guards (../ escape and symlink-escape are REFUSED)
#   · export (island -> host staging)
#   · Ledger: every crossing recorded, hash chain verifies, tamper is detected
#   · multi-agent: `init --agent X --agent Y` seeds both, a2 worktree reconciles
#
# It runs in a throwaway $HOME so it never touches your real ~/.dejima, and it
# purges the test islands + daemon on exit.
#
# Usage:   scripts/integration.sh
# Requires: docker (running), go, git. ~Several minutes on first run (image build).

set -uo pipefail

# ---------------------------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PASS=0 FAIL=0
pass(){ printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }
step(){ printf '\n\033[1m• %s\033[0m\n' "$*"; }
die(){ printf '\033[31mFATAL: %s\033[0m\n' "$*" >&2; exit 1; }

assert_eq(){ if [ "$1" = "$2" ]; then pass "$3"; else fail "$3 — got [$1] want [$2]"; fi; }
assert_has(){ if printf '%s' "$1" | grep -qF -- "$2"; then pass "$3"; else fail "$3 — missing [$2]"; fi; }
# expect_ok / expect_fail: run a command, assert its exit status, never abort.
expect_ok(){ local d="$1"; shift; if "$@" >/dev/null 2>&1; then pass "$d"; else fail "$d — command failed: $*"; fi; }
expect_fail(){ local d="$1"; shift; if "$@" >/dev/null 2>&1; then fail "$d — expected failure but it succeeded"; else pass "$d"; fi; }
# expect_err_match <desc> <substring> <cmd...>: assert the command FAILS *and*
# its output contains <substring> — so an unrelated non-zero exit can't pass for
# a security guard. Used for traversal/deny-all refusals.
expect_err_match(){
  local d="$1" want="$2"; shift 2
  local out rc
  out="$("$@" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    fail "$d — expected failure but it succeeded"
  elif printf '%s' "$out" | grep -qF -- "$want"; then
    pass "$d"
  else
    fail "$d — failed but error did not contain [$want]; got: $out"
  fi
}

command -v docker >/dev/null || die "docker not found / not running"
command -v go     >/dev/null || die "go not found"
command -v git    >/dev/null || die "git not found"
docker info >/dev/null 2>&1   || die "docker daemon not reachable"

# ---------------------------------------------------------------------------
# Isolated environment + cleanup
# ---------------------------------------------------------------------------
TMP="$(mktemp -d)"
REAL_HOME="$HOME"
export HOME="$TMP/home"
mkdir -p "$HOME"
# Docker's CLI config + contexts live in the real ~/.docker. The isolated HOME
# above would hide them — so on OrbStack/colima/Docker-Desktop the daemon can't
# resolve the Docker endpoint and image build fails. Point Docker back at the
# real config (DOCKER_HOST, if set, is already inherited from the environment).
export DOCKER_CONFIG="${DOCKER_CONFIG:-$REAL_HOME/.docker}"
BIN="$TMP/bin"; mkdir -p "$BIN"; export PATH="$BIN:$PATH"
ISLAND="itest-port"
ISLAND_MULTI="itest-multi"
DAEMON_PID=""
# The --local-copy seed is bind-mounted into the Docker VM, so it must live on a
# path Docker shares. macOS shares /Users by default but NOT /var/folders (where
# mktemp lands) — so put the test seed repo under the real home.
REPO_DIR="$REAL_HOME/.cache/dejima-itest-$$"
REPO="$REPO_DIR/repo"

cleanup(){
  set +e
  [ -n "$DAEMON_PID" ] && dejima purge "$ISLAND"       -f >/dev/null 2>&1
  [ -n "$DAEMON_PID" ] && dejima purge "$ISLAND_MULTI"  -f >/dev/null 2>&1
  [ -n "$DAEMON_PID" ] && kill "$DAEMON_PID" >/dev/null 2>&1
  # The isolated HOME holds a read-only Go module cache ($HOME/go/pkg/mod);
  # make the tree writable before removing it so cleanup exits silently.
  chmod -R u+w "$TMP" 2>/dev/null
  rm -rf "$TMP" "${REPO_DIR:-}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
step "Build dejima + dejimad"
( cd "$REPO_ROOT" && go build -o "$BIN/dejima" ./cmd/dejima && go build -o "$BIN/dejimad" ./cmd/dejimad ) \
  || die "build failed"
pass "binaries built into $BIN"

step "Start dejimad (isolated HOME=$HOME)"
dejimad --foreground >"$TMP/dejimad.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 50); do [ -S "$HOME/.dejima/dejimad.sock" ] && break; sleep 0.2; done
[ -S "$HOME/.dejima/dejimad.sock" ] || die "daemon socket never appeared (see $TMP/dejimad.log)"
dejima audit >/dev/null 2>&1 || die "daemon not responding"
pass "daemon up (pid $DAEMON_PID)"

step "Ensure island image"
if ! docker image inspect dejima/island:latest >/dev/null 2>&1; then
  echo "  building dejima/island:latest (first run, slow)…"
  dejima image build || die "image build failed (real error shown above)"
fi
pass "island image present"

step "Create a test repo + headless island (reliable, no-creds container)"
mkdir -p "$REPO"
( cd "$REPO" && git init -q && git config user.email t@t && git config user.name t \
  && echo "# test" > README.md && git add -A && git commit -qm init ) || die "test repo setup failed"
dejima init --name "$ISLAND" --repo "$REPO" --local-copy --agent headless --cmd "sleep infinity" \
  >/dev/null 2>&1 || die "island create failed (see $TMP/dejimad.log)"
pass "island $ISLAND created and running"

# ---------------------------------------------------------------------------
step "Scope: deny-all before any grant"
expect_err_match "intake refused before any scope is granted" "no Port scope" \
  dejima port intake "$ISLAND" "vault:note.md"

step "Set up a host scope with nested files + an escaping symlink"
SCOPE="$TMP/vault"
mkdir -p "$SCOPE/daily"
printf 'hello vault\n'   > "$SCOPE/note.md"
printf 'deep content\n'  > "$SCOPE/daily/2026.md"
printf 'locked content\n' > "$SCOPE/locked.md" && chmod 600 "$SCOPE/locked.md"  # 0600 → read-normalization test
printf 'TOP SECRET\n'    > "$TMP/secret.txt"          # OUTSIDE the scope
ln -s "$TMP/secret.txt"    "$SCOPE/escape"            # symlink that escapes the scope
pass "scope tree created at $SCOPE (basename → scope 'vault')"

step "Grant the scope (read-only)"
expect_ok "grant vault:ro" dejima port grant "$ISLAND" "$SCOPE:ro"
LIST="$(dejima port list "$ISLAND" 2>&1)"
assert_has "$LIST" "vault" "scope appears in \`port list\`"
assert_has "$LIST" "ro"    "scope is read-only"

step "Intake: host → island copy"
expect_ok "intake note.md" dejima port intake "$ISLAND" "vault:note.md"
GOT="$(dejima exec "$ISLAND" -- cat /home/dejima/intake/vault/note.md 2>/dev/null)"
assert_eq "$GOT" "hello vault" "note.md content landed inside the island"

expect_ok "intake nested daily/2026.md" dejima port intake "$ISLAND" "vault:daily/2026.md"
GOT="$(dejima exec "$ISLAND" -- cat /home/dejima/intake/vault/daily/2026.md 2>/dev/null)"
assert_eq "$GOT" "deep content" "nested file landed at the mirrored path"

expect_ok "intake to a custom dest" dejima port intake "$ISLAND" "vault:note.md" "/tmp/custom.md"
GOT="$(dejima exec "$ISLAND" -- cat /tmp/custom.md 2>/dev/null)"
assert_eq "$GOT" "hello vault" "custom dest honored"

# A 0600 host file must still be readable by the agent (uid 1000 ≠ host owner) —
# intake normalizes the in-island copy to 0644 (read-normalization).
expect_ok "intake 0600 host file" dejima port intake "$ISLAND" "vault:locked.md"
GOT="$(dejima exec "$ISLAND" -- cat /home/dejima/intake/vault/locked.md 2>/dev/null)"
assert_eq "$GOT" "locked content" "0600 host file is agent-readable after read-normalization"

step "Traversal guards: escapes must be REFUSED (with the right error)"
expect_err_match "../ parent traversal refused as scope-escape" "escapes the scope" \
  dejima port intake "$ISLAND" "vault:../secret.txt"
expect_err_match "symlink-escape refused as scope-escape" "escapes the scope" \
  dejima port intake "$ISLAND" "vault:escape"
# And prove the secret never crossed:
if dejima exec "$ISLAND" -- sh -c 'cat /intake/vault/escape 2>/dev/null; cat /intake/vault/../../etc/hostname 2>/dev/null' 2>/dev/null | grep -q "TOP SECRET"; then
  fail "secret leaked into the island"
else
  pass "secret never crossed the broker"
fi

step "Export: island → host staging"
dejima exec "$ISLAND" -- sh -c 'echo exported-data > /tmp/out.txt' >/dev/null 2>&1 || die "could not write file in island"
expect_ok "export /tmp/out.txt" dejima port export "$ISLAND" "/tmp/out.txt"
EXP="$HOME/.dejima/projects/$ISLAND/exports/out.txt"
if [ -f "$EXP" ]; then
  assert_eq "$(cat "$EXP")" "exported-data" "exported file content matches in staging"
else
  fail "export did not land in $EXP"
fi

# ---------------------------------------------------------------------------
step "Ledger: every crossing recorded + hash chain verifies"
LEDGER="$HOME/.dejima/ledger.jsonl"
[ -f "$LEDGER" ] || die "ledger file missing"
AUDIT="$(dejima audit 2>&1)"
assert_has "$AUDIT" "port.grant"   "grant recorded in ledger"
assert_has "$AUDIT" "trade.read"   "intake recorded as trade.read"
assert_has "$AUDIT" "trade.export" "export recorded as trade.export"
expect_ok "hash chain verifies clean" dejima audit --verify

step "Ledger locking: tampering is DETECTED"
cp "$LEDGER" "$TMP/ledger.bak"
# Flip a recorded field in the first entry without recomputing its chain hash.
sed -i.orig 's/"decision":"allowed"/"decision":"ALLOWED"/' "$LEDGER" 2>/dev/null \
  || sed -i 's/"decision":"allowed"/"decision":"ALLOWED"/' "$LEDGER"
expect_fail "tampered ledger fails verification" dejima audit --verify
cp "$TMP/ledger.bak" "$LEDGER"   # restore exact bytes (in-memory chain head still matches)
expect_ok "restored ledger verifies again" dejima audit --verify

# ---------------------------------------------------------------------------
step "Revoke → back to deny-all"
expect_ok "revoke vault" dejima port revoke "$ISLAND" "vault"
expect_err_match "intake refused after revoke" "no Port scope" \
  dejima port intake "$ISLAND" "vault:note.md"

# ---------------------------------------------------------------------------
step "Multi-agent: seed two agents at create time"
dejima init --name "$ISLAND_MULTI" --repo "$REPO" --local-copy --agent claude-code --agent codex \
  >/dev/null 2>&1 || die "multi-agent island create failed"
AGENTS="$(dejima agent ls "$ISLAND_MULTI" 2>&1)"
assert_has "$AGENTS" "claude-code" "primary agent a1 = claude-code"
assert_has "$AGENTS" "codex"       "seeded agent a2 = codex"
# a2's worktree is reconciled asynchronously after create returns — poll for it.
wt_ok=""
for _ in $(seq 1 30); do
  if dejima exec "$ISLAND_MULTI" -- test -d /workspace/.agents/a2 >/dev/null 2>&1; then wt_ok=1; break; fi
  sleep 1
done
if [ -n "$wt_ok" ]; then
  pass "a2 worktree exists in-container"
else
  fail "a2 worktree never appeared in-container (waited 30s)"
  printf '\033[33m  ── diagnostics ──\033[0m\n'
  printf '  /workspace/.git present? %s\n' "$(dejima exec "$ISLAND_MULTI" -- sh -c 'test -e /workspace/.git && echo YES || echo NO' 2>&1)"
  printf '  /workspace listing:\n'; dejima exec "$ISLAND_MULTI" -- ls -la /workspace 2>&1 | sed 's/^/    /'
  printf '  agent table (look at a2 ERROR column):\n'; dejima agent ls "$ISLAND_MULTI" 2>&1 | sed 's/^/    /'
  printf '  daemon log (worktree/reconcile/clone lines):\n'
  grep -iE "worktree|ensure agent|reconcile|clone|seed|not a git" "$TMP/dejimad.log" 2>/dev/null | tail -20 | sed 's/^/    /'
fi

# ---------------------------------------------------------------------------
printf '\n\033[1m──────── results ────────\033[0m\n'
printf 'passed: %d   failed: %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || { printf '\033[31mINTEGRATION TEST FAILED\033[0m\n'; exit 1; }
printf '\033[32mALL GREEN\033[0m\n'
