#!/usr/bin/env bash
# Dejima end-to-end integration test — the DETERMINISTIC FULL-FEATURE RUN.
#
# A single dispatch exercises every Tier-2 feature once, in a sensible order,
# against real containers, with clear per-feature pass/fail:
#   · bootstrap: build, daemon up, island image, create
#   · lifecycle: ls/status/exec/hibernate/wake/upgrade
#   · Port (brokered host-file access): scope grant / deny-all; intake (host ->
#     island) incl. nested paths + custom dest; traversal guards (../ + symlink
#     escape REFUSED); export (island -> host); read-write; Ledger record +
#     hash-chain verify + tamper detection; audit jsonl/csv export
#   · MCP brokering: deny-all / grant / brokered call / ledger / revoke
#   · multi-agent: `init --agent X --agent Y` seeds both, a2 worktree reconciles
#   · clone an island's workspace into a new island
#   · inter-island exchange: link/msg, action gate, approve/deny, ledger
#   · purge unpushed-work guard + force-purge override
#   · capability brokering: deny-all / grant / list / revoke
#   · provider credentials: set / list (masked) / rm
#   · team tokens: mint + list (role *enforcement* is roleauth_test.go's job —
#     it can't be exercised over the owner-trusted local socket)
#   · webhooks: events catalog / subscribe / ls / rm
#   · activity feed · panic brake (engage/status/clear) · doctor health check
#
# It runs in a throwaway $HOME so it never touches your real ~/.dejima, and it
# purges the test islands + daemon on exit.
#
# Structured reporting: set DEJIMA_REPORT=<path> to also emit a machine-readable
# JSON summary (pass/fail per feature) for the CI artifact + issue-on-failure
# step. See scripts/lib/report.sh and docs/testing/full-suite-design.md.
#
# Usage:   scripts/integration.sh   (optionally: DEJIMA_REPORT=report.json …)
# Requires: docker (running), go, git. ~Several minutes on first run (image build).

set -uo pipefail

# ---------------------------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# PASS/FAIL are the tally counters; report.sh (sourced below) reads and updates
# them. shellcheck can't see that through source=/dev/null, hence the disable.
# shellcheck disable=SC2034
PASS=0 FAIL=0
step(){ printf '\n\033[1m• %s\033[0m\n' "$*"; }
# die prints the failure AND dumps the tail of the daemon log to stderr, so a
# fatal (e.g. island create failed) is diagnosable straight from the terminal
# output — no scrolling a tmux pane or hunting a temp file. $TMP may be unset for
# very-early failures (before the daemon starts); guard for it.
die(){
	printf '\033[31mFATAL: %s\033[0m\n' "$*" >&2
	if [ -n "${TMP:-}" ] && [ -f "$TMP/dejimad.log" ]; then
		printf '\033[31m--- last 50 daemon-log lines ---\033[0m\n' >&2
		tail -50 "$TMP/dejimad.log" >&2
		printf '\033[31m--- end daemon log ---\033[0m\n' >&2
	fi
	exit 1
}

# with_progress LABEL CMD...: run CMD, and on an interactive terminal show a live
# "⏳ LABEL… (Ns)" elapsed counter on one line — proof the step is busy, not
# blocked on input — cleared when CMD finishes. On a non-TTY (CI) it prints one
# plain line and runs CMD directly (no spinner spam in logs). CMD's exit status
# is preserved. Use it to wrap the silent multi-second steps (go build).
with_progress(){
	local label="$1"; shift
	if [ ! -t 2 ]; then
		printf '  … %s\n' "$label" >&2
		"$@"
		return $?
	fi
	"$@" &
	local pid=$! s=0
	while kill -0 "$pid" 2>/dev/null; do
		printf '\r\033[K  \033[2m⏳ %s… (%ds)\033[0m' "$label" "$s" >&2
		sleep 1
		s=$((s + 1))
	done
	wait "$pid"
	local rc=$?
	printf '\r\033[K' >&2
	return "$rc"
}

# build_dejima_binaries compiles the CLI + daemon into $BIN (shared by bootstrap
# and the re-adopt reinstall step).
build_dejima_binaries(){
	( cd "$REPO_ROOT" && go build -o "$BIN/dejima" ./cmd/dejima && go build -o "$BIN/dejimad" ./cmd/dejimad )
}

# wait_for_daemon polls for the dejimad unix socket (up to ~10s), showing a live
# elapsed tick on a TTY so the wait isn't mistaken for a hang. 0 once the socket
# appears, non-zero on timeout.
wait_for_daemon(){
	local label="${1:-starting dejimad}" i=0
	while [ "$i" -lt 50 ]; do
		if [ -S "$HOME/.dejima/dejimad.sock" ]; then
			[ -t 2 ] && printf '\r\033[K' >&2
			return 0
		fi
		[ -t 2 ] && printf '\r\033[K  \033[2m⏳ %s… (%ds)\033[0m' "$label" "$((i / 5))" >&2
		sleep 0.2
		i=$((i + 1))
	done
	[ -t 2 ] && printf '\r\033[K' >&2
	[ -S "$HOME/.dejima/dejimad.sock" ]
}
# Structured per-feature reporting (feature/pass/fail tally + JSON summary when
# DEJIMA_REPORT is set). report.sh defines feature(), and pass()/fail() that
# tally per-feature as well as globally — source it so those versions win.
DEJIMA_SUITE="${DEJIMA_SUITE:-tier2-integration}"
# report.sh is sourced at runtime; source=/dev/null stops shellcheck from
# trying to follow it (the CI lint job runs without -x and lints report.sh on
# its own line — see .github/workflows/ci.yml).
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/lib/report.sh"

assert_eq(){ if [ "$1" = "$2" ]; then pass "$3"; else fail "$3 — got [$1] want [$2]"; fi; }
assert_has(){ if printf '%s' "$1" | grep -qF -- "$2"; then pass "$3"; else fail "$3 — missing [$2]"; fi; }
# expect_ok / expect_fail: run a command, assert its exit status, never abort.
# Capture the command's output and surface it (squashed to one line) in the
# failure detail, so a red feature is self-explanatory in the run summary + the
# filed tracking issue instead of needing the daemon log to diagnose.
expect_ok(){ local d="$1"; shift; local out rc; out="$("$@" 2>&1)"; rc=$?; out="$(printf '%s' "$out" | tr '\n' ' ')"; if [ "$rc" -eq 0 ]; then pass "$d"; else fail "$d — command failed (rc=$rc): $* — ${out}"; fi; }
expect_fail(){ local d="$1"; shift; local out rc; out="$("$@" 2>&1)"; rc=$?; out="$(printf '%s' "$out" | tr '\n' ' ')"; if [ "$rc" -eq 0 ]; then fail "$d — expected failure but it succeeded; output: ${out}"; else pass "$d"; fi; }
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
# run_bounded <secs> <cmd...>: run cmd under a portable wall-clock deadline
# (macOS ships no GNU `timeout`). stdout passes through so it stays $(...)-
# capturable. On timeout the child is killed and 124 returned; otherwise the
# child's own exit status. Guards suite-direct `docker run`s, which can wedge the
# whole job for 30+ min on a stalled engine instead of failing fast.
run_bounded(){
  local secs="$1"; shift
  "$@" &
  local pid=$! waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge "$secs" ]; then
      kill -9 "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      return 124
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"
}

command -v docker >/dev/null || die "docker not found / not running"
command -v go     >/dev/null || die "go not found"
command -v git    >/dev/null || die "git not found"
docker info >/dev/null 2>&1   || die "docker daemon not reachable"

# Live-island guard. This suite churns Docker (creates/purges islands + throwaway
# `docker run`s) on whatever engine `docker` points at, and recovering a wedged
# engine means `colima restart` — which stops EVERY container on that VM. If real
# (non-itest) dejima islands are present, this engine is shared with live work
# (e.g. aoos's agents on aoos's colima): refuse, and point at the isolated
# dejimaqa test account (its own colima).
#
# Skipped in CI ($GITHUB_ACTIONS): on the dejimaqa runner every dejima container
# is a test artifact — including OTHER suites' non-itest islands (c2's
# dejima-tui-*, the agent smoke's) that may linger after a failed job — so a real
# refusal there would be a false positive. DEJIMA_ITEST_ALLOW_LIVE=1 also
# overrides, for a knowing operator on a non-CI host.
if [ "${DEJIMA_ITEST_ALLOW_LIVE:-}" != "1" ] && [ -z "${GITHUB_ACTIONS:-}" ]; then
  live_islands="$(docker ps -a --filter label=dejima.project --format '{{.Names}}' 2>/dev/null | grep -v '^dejima-itest-' || true)"
  if [ -n "$live_islands" ]; then
    printf '\033[31mFATAL: live dejima islands are present on this Docker engine:\033[0m\n' >&2
    printf '%s\n' "$live_islands" | sed 's/^/  - /' >&2
    printf 'This suite churns Docker and recovering a wedged engine needs a colima restart,\n' >&2
    printf 'which would STOP those islands. Run it on the isolated dejimaqa test account\n' >&2
    printf '(its own colima), or set DEJIMA_ITEST_ALLOW_LIVE=1 to override.\n' >&2
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# Install-channel sanity (Lane A). No Docker needed; runs first so a broken
# install path (the #1 clean-Mac failure mode) is caught before the heavier,
# slower container suite below. The end-to-end virgin-Mac proof is the separate
# human-run Lane 0 procedure; this is the part we can automate.
# ---------------------------------------------------------------------------
feature "install channels (curl|sh / brew / npm consistency)"
if bash "$REPO_ROOT/scripts/install-channels-check.sh"; then
  pass "install-channel consistency check"
else
  fail "install-channel consistency check (see output above)"
fi

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
ISLAND_CLONE="itest-clone"
ISLAND_A="itest-a"
ISLAND_B="itest-b"
ISLAND_GUARD="itest-guard"
ISLAND_READOPT="itest-readopt"
DAEMON_PID=""
# The --local-copy seed is bind-mounted into the Docker VM, so it must live on a
# path Docker shares. macOS shares /Users by default but NOT /var/folders (where
# mktemp lands) — so put the test seed repo under the real home.
REPO_DIR="$REAL_HOME/.cache/dejima-itest-$$"
REPO="$REPO_DIR/repo"

# force_remove_itest_docker tears down every test island's docker objects
# DIRECTLY (no daemon needed). A crashed/Ctrl-C'd run kills the daemon before
# `dejima purge` can run, orphaning `dejima-itest-*` containers — then the next
# run's `docker run --name` fails with exit 125 ("name already in use"). Scoped
# to the exact itest-* names so real islands are never touched.
force_remove_itest_docker(){
  local isl
  for isl in "$ISLAND" "$ISLAND_MULTI" "$ISLAND_CLONE" "$ISLAND_A" "$ISLAND_B" "$ISLAND_GUARD" "$ISLAND_READOPT"; do
    docker rm -f "dejima-$isl" >/dev/null 2>&1 || true
    docker volume rm -f "dejima-$isl-workspace" "dejima-$isl-home" >/dev/null 2>&1 || true
    docker network rm "dejima-net-$isl" >/dev/null 2>&1 || true
  done
}

cleanup(){
  set +e
  for isl in "$ISLAND" "$ISLAND_MULTI" "$ISLAND_CLONE" "$ISLAND_A" "$ISLAND_B" "$ISLAND_GUARD" "$ISLAND_READOPT"; do
    [ -n "$DAEMON_PID" ] && dejima purge "$isl" -f >/dev/null 2>&1
  done
  # Backstop the graceful purge above with a direct docker teardown, so a run
  # that dies before/while the daemon is up still leaves no orphaned containers
  # or volumes to collide with the next run.
  force_remove_itest_docker
  [ -n "$DAEMON_PID" ] && kill "$DAEMON_PID" >/dev/null 2>&1
  # Preserve the daemon log at a STABLE path before nuking $TMP, so a failure can
  # be diagnosed by `cat`-ing one file instead of scrolling a tmux pane.
  # Use REAL_HOME, not the isolated $HOME (which is $TMP/home — about to be rm'd).
  cp -f "$TMP/dejimad.log" "$REAL_HOME/dejima-itest-dejimad.log" 2>/dev/null \
    && echo "daemon log saved to $REAL_HOME/dejima-itest-dejimad.log"
  # The isolated HOME holds a read-only Go module cache ($HOME/go/pkg/mod);
  # make the tree writable before removing it so cleanup exits silently.
  chmod -R u+w "$TMP" 2>/dev/null
  rm -rf "$TMP" "${REPO_DIR:-}"
}
# EXIT alone misses Ctrl-C: an uncaught SIGINT terminates the script WITHOUT
# running the EXIT trap, so a hand-interrupted run wouldn't save the daemon log
# (or tear down islands). Make INT/TERM exit cleanly, which fires the EXIT trap
# (cleanup) exactly once.
trap cleanup EXIT
trap 'exit 130' INT TERM

# Startup sweep: clear any dejima-itest-* docker objects a previously interrupted
# run left behind, so the first `docker run --name` doesn't 125 on a name
# conflict. No-op (silenced) when docker isn't up yet — bootstrap checks that.
force_remove_itest_docker

# ---------------------------------------------------------------------------
feature "bootstrap (build/daemon/image/create)"
step "Build dejima + dejimad"
with_progress "compiling dejima + dejimad" build_dejima_binaries || die "build failed"
pass "binaries built into $BIN"

step "Start dejimad (isolated HOME=$HOME)"
dejimad --foreground >"$TMP/dejimad.log" 2>&1 &
DAEMON_PID=$!
wait_for_daemon || die "daemon socket never appeared (see $TMP/dejimad.log)"
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
# Lifecycle — list/status/exec, and the hibernate→wake→upgrade→reset verbs
# against the real container. These are the verbs the manual Minion pass walks;
# automating them here shrinks that queue.
# ---------------------------------------------------------------------------
feature "lifecycle (ls/status/exec/hibernate/wake/upgrade)"
step "Lifecycle: ls + status show the island"
LS="$(dejima ls 2>&1)"
assert_has "$LS" "$ISLAND" "island appears in \`dejima ls\`"
ST="$(dejima status "$ISLAND" 2>&1)"
assert_has "$ST" "$ISLAND" "\`dejima status\` shows the island"

step "Lifecycle: exec runs a command inside the island"
EX="$(dejima exec "$ISLAND" -- sh -c 'echo exec-works' 2>/dev/null)"
assert_eq "$EX" "exec-works" "exec returns the command's stdout"

step "Lifecycle: hibernate stops the container, wake brings it back"
expect_ok "hibernate" dejima hibernate "$ISLAND"
# After hibernate the container is not running; exec should fail until woken.
expect_fail "exec refused while hibernated" dejima exec "$ISLAND" -- true
expect_ok "wake" dejima wake "$ISLAND"
# Poll for the woken container to accept exec again.
woke=""
for _ in $(seq 1 30); do
  if dejima exec "$ISLAND" -- true >/dev/null 2>&1; then woke=1; break; fi
  sleep 1
done
if [ -n "$woke" ]; then pass "exec works again after wake"; else fail "island never became exec-able after wake"; fi

step "Lifecycle: upgrade recreates on the current image (state preserved)"
# Write a workspace marker, upgrade, and confirm it survives the recreate.
dejima exec "$ISLAND" -- sh -c 'echo survive > /workspace/marker.txt' >/dev/null 2>&1 \
  || die "could not write workspace marker"
expect_ok "upgrade" dejima upgrade "$ISLAND"
upgraded=""
for _ in $(seq 1 30); do
  if dejima exec "$ISLAND" -- test -f /workspace/marker.txt >/dev/null 2>&1; then upgraded=1; break; fi
  sleep 1
done
if [ -n "$upgraded" ]; then
  GOT="$(dejima exec "$ISLAND" -- cat /workspace/marker.txt 2>/dev/null)"
  assert_eq "$GOT" "survive" "workspace survived the upgrade recreate"
else
  fail "island never came back exec-able after upgrade"
fi

# ---------------------------------------------------------------------------
feature "port — host-file brokering (scope/intake/export/rw/traversal/ledger)"
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
step "Read-write: write island → :rw host scope"
# A :ro scope refuses writes; a :rw scope accepts them.
expect_err_match "write refused to a read-only scope" "read-only" \
  dejima port write "$ISLAND" /tmp/out.txt "vault:back.md"
RWDIR="$REPO_DIR/rwscope"; mkdir -p "$RWDIR"
expect_ok "grant rwscope:rw" dejima port grant "$ISLAND" "$RWDIR:rw"
dejima exec "$ISLAND" -- sh -c 'echo written-from-island > /tmp/w.txt' >/dev/null 2>&1 || die "could not write file in island"
expect_ok "write into :rw scope" dejima port write "$ISLAND" /tmp/w.txt "rwscope:notes/w.md"
if [ -f "$RWDIR/notes/w.md" ]; then
  assert_eq "$(cat "$RWDIR/notes/w.md")" "written-from-island" "written file landed on the host"
else
  fail "write did not land at $RWDIR/notes/w.md"
fi
expect_err_match "write ../ escape refused" "escapes the scope" \
  dejima port write "$ISLAND" /tmp/w.txt "rwscope:../escape.md"

# ---------------------------------------------------------------------------
step "Ledger: every crossing recorded + hash chain verifies"
LEDGER="$HOME/.dejima/ledger.jsonl"
[ -f "$LEDGER" ] || die "ledger file missing"
AUDIT="$(dejima audit 2>&1)"
assert_has "$AUDIT" "port.grant"   "grant recorded in ledger"
assert_has "$AUDIT" "trade.read"   "intake recorded as trade.read"
assert_has "$AUDIT" "trade.export" "export recorded as trade.export"
expect_ok "hash chain verifies clean" dejima audit --verify

step "Audit export: jsonl + csv stream the filtered records"
JSONL="$(dejima audit --export jsonl 2>/dev/null)"
assert_has "$JSONL" '"type":"port.grant"' "jsonl export contains the port.grant record"
CSV="$(dejima audit --export csv 2>/dev/null)"
assert_has "$CSV" "port.grant" "csv export contains the port.grant record"

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
# MCP broker — deny-all grants of named, host-curated MCP servers + a brokered,
# ledgered call path (the Port/capability pattern applied to MCP servers). The
# broker spawns the server program on the daemon host and speaks JSON-RPC over
# its stdio, so it exercises the real grant/call/ledger path with no container
# round-trip. See docs/mcp-broker-spec.md.
feature "mcp brokering (deny-all/grant/call/ledger/revoke)"
step "MCP broker: build a mock stdio MCP server (host-curated)"
# A minimal newline-delimited JSON-RPC 2.0 MCP server, stdlib-only so it builds
# anywhere `go` runs (no python/jq dependency — Minion is macOS).
cat > "$TMP/mock-mcp.go" <<'GO'
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	send := func(v any) { b, _ := json.Marshal(v); fmt.Printf("%s\n", b) }
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": rawID(m.ID), "result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "mock", "version": "1"}}})
		case "notifications/initialized":
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": rawID(m.ID), "result": map[string]any{"tools": []map[string]any{{"name": "echo"}}}})
		case "tools/call":
			text := "called " + m.Params.Name + " island=" + os.Getenv("DEJIMA_MCP_ISLAND")
			send(map[string]any{"jsonrpc": "2.0", "id": rawID(m.ID), "result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": m.Params.Name == "boom"}})
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": rawID(m.ID), "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}
}

func rawID(raw json.RawMessage) any {
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return string(raw)
}
GO
( cd "$REPO_ROOT" && go build -o "$BIN/mock-mcp" "$TMP/mock-mcp.go" ) || die "mock MCP server build failed"
# Curate it host-side — the registry file is the trust boundary (owned by the
# daemon user, not world-writable). The island cannot write here.
mkdir -p "$HOME/.dejima/mcp"
cat > "$HOME/.dejima/mcp/servers.toml" <<EOF
[[servers]]
name = "mock"
transport = "stdio"
command = "$BIN/mock-mcp"
EOF
chmod 600 "$HOME/.dejima/mcp/servers.toml"
pass "mock MCP server registered as 'mock'"

step "MCP broker: deny-all before any grant"
expect_err_match "call refused before any grant" "not granted" \
  dejima mcp call "$ISLAND" mock --method tools/list

step "MCP broker: grant + brokered calls"
expect_ok "grant mock" dejima mcp grant "$ISLAND" mock
MLIST="$(dejima mcp ls "$ISLAND" 2>&1)"
assert_has "$MLIST" "mock" "granted server appears in \`mcp ls\`"
TOOLS="$(dejima mcp call "$ISLAND" mock --method tools/list 2>&1)"
assert_has "$TOOLS" "echo" "tools/list returns the server's tools"
CALL="$(dejima mcp call "$ISLAND" mock --method tools/call --params '{"name":"echo"}' 2>&1)"
assert_has "$CALL" "called echo" "tools/call result returned to the caller"
assert_has "$CALL" "island=$ISLAND" "the island identity reached the server via the broker env"

step "MCP broker: only the brokered method surface is callable"
expect_err_match "lifecycle method refused" "not permitted" \
  dejima mcp call "$ISLAND" mock --method initialize

step "MCP broker: every call is ledgered (mcp.*) + chain verifies"
MAUDIT="$(dejima audit 2>&1)"
assert_has "$MAUDIT" "mcp.grant" "grant recorded as mcp.grant"
assert_has "$MAUDIT" "mcp.call"  "brokered call recorded as mcp.call"
assert_has "$MAUDIT" "mcp.deny"  "refused call recorded as mcp.deny"
expect_ok "ledger chain still verifies with mcp.* entries" dejima audit --verify

step "MCP broker: revoke → back to deny-all"
expect_ok "revoke mock" dejima mcp revoke "$ISLAND" mock
expect_err_match "call refused after revoke" "not granted" \
  dejima mcp call "$ISLAND" mock --method tools/list

# ---------------------------------------------------------------------------
feature "multi-agent create-time seeding"
step "Multi-agent: seed two agents at create time"
dejima init --name "$ISLAND_MULTI" --repo "$REPO" --local-copy --agent claude-code --agent codex \
  >/dev/null 2>&1 || die "multi-agent island create failed"
AGENTS="$(dejima agent ls "$ISLAND_MULTI" 2>&1)"
assert_has "$AGENTS" "claude-code" "primary agent = claude-code"
assert_has "$AGENTS" "codex"       "seeded secondary agent = codex"
# The secondary agent's worktree is reconciled asynchronously after create
# returns — poll for it. id-scheme-agnostic: any reconciled worktree under
# /workspace/.agents/<id> carries a .git pointer file (don't hardcode the id,
# which is no longer "a2" under the current scheme).
wt_ok=""
for _ in $(seq 1 30); do
  if dejima exec "$ISLAND_MULTI" -- sh -c 'ls /workspace/.agents/*/.git >/dev/null 2>&1'; then wt_ok=1; break; fi
  sleep 1
done
if [ -n "$wt_ok" ]; then
  pass "secondary-agent worktree exists in-container"
else
  fail "secondary-agent worktree never appeared in-container (waited 30s)"
  printf '\033[33m  ── diagnostics ──\033[0m\n'
  printf '  /workspace/.git present? %s\n' "$(dejima exec "$ISLAND_MULTI" -- sh -c 'test -e /workspace/.git && echo YES || echo NO' 2>&1)"
  printf '  /workspace listing:\n'; dejima exec "$ISLAND_MULTI" -- ls -la /workspace 2>&1 | sed 's/^/    /'
  printf '  agent table (check the secondary agent row):\n'; dejima agent ls "$ISLAND_MULTI" 2>&1 | sed 's/^/    /'
  printf '  daemon log (worktree/reconcile/clone lines):\n'
  grep -iE "worktree|ensure agent|reconcile|clone|seed|not a git" "$TMP/dejimad.log" 2>/dev/null | tail -20 | sed 's/^/    /'
fi

# ---------------------------------------------------------------------------
# Clone — a byte-for-byte copy of an island's workspace into fresh volumes; the
# clone starts deny-all (Port grants are NOT carried over). See `dejima clone`.
# ---------------------------------------------------------------------------
feature "clone an island"
step "Clone: duplicate an island's workspace into a new island"
# Leave a marker in the source workspace so we can prove the copy is faithful.
dejima exec "$ISLAND" -- sh -c 'echo cloned-content > /workspace/clone-marker.txt' >/dev/null 2>&1 \
  || die "could not write clone marker"
expect_ok "clone island" dejima clone "$ISLAND" "$ISLAND_CLONE"
cloned=""
for _ in $(seq 1 60); do
  if dejima exec "$ISLAND_CLONE" -- test -f /workspace/clone-marker.txt >/dev/null 2>&1; then cloned=1; break; fi
  sleep 1
done
if [ -n "$cloned" ]; then
  GOT="$(dejima exec "$ISLAND_CLONE" -- cat /workspace/clone-marker.txt 2>/dev/null)"
  assert_eq "$GOT" "cloned-content" "clone carried the source workspace"
else
  fail "clone never became exec-able with the copied workspace (waited 60s)"
fi

# ---------------------------------------------------------------------------
# Inter-island exchange (Lane 5) — cross-island is deny-all; a channel exists
# only as an explicit, directional, operator-granted A→B grant on a topic. An
# action is delivered immediately when pre-authorized, else queued for operator
# approval. See docs/inter-island-exchange-spec.md.
# ---------------------------------------------------------------------------
feature "inter-island exchange (link/msg/action-gate/approve/deny/ledger)"
step "Inter-island: create two islands A and B"
dejima init --name "$ISLAND_A" --repo "$REPO" --local-copy --agent headless --cmd "sleep infinity" \
  >/dev/null 2>&1 || die "island A create failed"
dejima init --name "$ISLAND_B" --repo "$REPO" --local-copy --agent headless --cmd "sleep infinity" \
  >/dev/null 2>&1 || die "island B create failed"
# The recipient agent id (headless primary) on B, for addressed sends.
B_AGENT="$(dejima agent ls "$ISLAND_B" 2>/dev/null | awk 'NR==2{print $1}')"
[ -n "$B_AGENT" ] || B_AGENT="a1"
pass "islands A + B created (B primary agent: $B_AGENT)"

step "Inter-island: deny-all before any grant"
expect_err_match "send refused before any grant" "deny-all" \
  dejima link send "$ISLAND_B" "$B_AGENT" "hi" --from "$ISLAND_A" --topic ops

step "Inter-island: grant a directional channel + deliver an info message"
expect_ok "grant A→B/ops" dejima link grant "$ISLAND_A" "$ISLAND_B" --topic ops
LINKS="$(dejima link ls 2>&1)"
assert_has "$LINKS" "$ISLAND_A" "grant appears in \`link ls\`"
assert_has "$LINKS" "ops"       "grant topic recorded"
expect_ok "send over the granted channel" \
  dejima link send "$ISLAND_B" "$B_AGENT" "hello B" --from "$ISLAND_A" --from-agent a1 --topic ops
# The message lands in B's ordinary mailbox, stamped cross-island.
INBOX="$(dejima msg poll --island "$ISLAND_B" --agent "$B_AGENT" 2>&1)"
assert_has "$INBOX" "hello B" "cross-island message delivered into B's mailbox"

step "Inter-island: directional — the reverse channel is NOT implied"
expect_err_match "reverse send refused" "deny-all" \
  dejima link send "$ISLAND_A" a1 "reply" --from "$ISLAND_B" --topic ops

step "Inter-island: action gate — exposed + pre-authorized executes; else queues"
# An action not exposed by B is refused even with a channel grant.
expect_fail "unexposed action refused" \
  dejima link action "$ISLAND_B" "$B_AGENT" deploy --from "$ISLAND_A" --topic ops
expect_ok "B exposes deploy" dejima link expose "$ISLAND_B" deploy
EXPOSED="$(dejima link exposed "$ISLAND_B" 2>&1)"
assert_has "$EXPOSED" "deploy" "exposed action listed"
# Exposed but not pre-authorized on the grant → queued for operator approval.
ACT="$(dejima link action "$ISLAND_B" "$B_AGENT" deploy --from "$ISLAND_A" --topic ops 2>&1)"
assert_has "$ACT" "queued" "non-pre-authorized action is queued"
APPROVALS="$(dejima link approvals 2>&1)"
assert_has "$APPROVALS" "deploy" "queued action appears in operator approvals"
# Approve it → executes and delivers as a typed action.
PID="$(dejima link approvals 2>/dev/null | awk 'NR==2{print $1}')"
if [ -n "$PID" ]; then
  expect_ok "operator approves the queued action" dejima link approve "$PID"
else
  fail "could not read a pending action id to approve"
fi

step "Inter-island: deny path — a queued action can be denied (fail-closed)"
ACT2="$(dejima link action "$ISLAND_B" "$B_AGENT" deploy --from "$ISLAND_A" --topic ops 2>&1)"
assert_has "$ACT2" "queued" "second action queued"
PID2="$(dejima link approvals 2>/dev/null | awk 'NR==2{print $1}')"
if [ -n "$PID2" ]; then
  expect_ok "operator denies the queued action" dejima link deny "$PID2"
  expect_fail "approving a denied id fails closed" dejima link approve "$PID2"
else
  fail "could not read a pending action id to deny"
fi

step "Inter-island: revoke → back to deny-all"
expect_ok "revoke A→B/ops" dejima link revoke "$ISLAND_A" "$ISLAND_B" --topic ops
expect_err_match "send refused after revoke" "deny-all" \
  dejima link send "$ISLAND_B" "$B_AGENT" "hi" --from "$ISLAND_A" --topic ops

step "Inter-island: every decision is ledgered + chain still verifies"
XAUDIT="$(dejima audit 2>&1)"
assert_has "$XAUDIT" "link.grant"   "grant recorded as link.grant"
assert_has "$XAUDIT" "link.message" "delivery recorded as link.message"
assert_has "$XAUDIT" "link.deny"    "refused send recorded as link.deny"
expect_ok "ledger chain verifies with link.* entries" dejima audit --verify

# ---------------------------------------------------------------------------
# Purge unpushed-work guard — purging an island with unpushed/uncommitted work
# is refused unless forced. The guard is what stops an accidental purge from
# silently dropping an agent's work.
# ---------------------------------------------------------------------------
feature "purge unpushed-work guard + force-purge"
step "Purge guard: unpushed work blocks a plain purge, --force overrides"
dejima init --name "$ISLAND_GUARD" --repo "$REPO" --local-copy --agent headless --cmd "sleep infinity" \
  >/dev/null 2>&1 || die "guard island create failed"
# Leave UNCOMMITTED work in the workspace. The daemon's guard counts
# `git status --porcelain` lines, so an untracked file makes the tree dirty
# (DirtyFiles>0). NOTE: a local *commit* on a branch with no upstream would NOT
# trip the guard — the tree is clean and Ahead needs an upstream — which is a
# separate product gap (committed-but-nowhere-pushed work isn't flagged).
dejima exec "$ISLAND_GUARD" -- sh -c 'echo work > /workspace/uncommitted.txt' \
  >/dev/null 2>&1 || die "could not create uncommitted work in the guard island"
# The CLI now gates a plain purge behind a type-the-island-name confirmation, so
# feed the name on stdin to get PAST it and exercise the DAEMON's unpushed-work
# guard — the layer that emits the `--force` hint.
purge_confirmed(){ printf '%s\n' "$1" | dejima purge "$1"; }
expect_err_match "plain purge refused with unpushed work" "--force" \
  purge_confirmed "$ISLAND_GUARD"
expect_ok "force-purge overrides the guard" dejima purge "$ISLAND_GUARD" --force
expect_fail "guard island is gone after force-purge" dejima status "$ISLAND_GUARD"

# ---------------------------------------------------------------------------
# Capability brokering — host actions are deny-all until granted; a grant shows
# up in the list and a revoke returns to deny-all. Every decision is ledgered.
# ---------------------------------------------------------------------------
feature "capability brokering (deny-all/grant/list/revoke + ledger)"
step "Capability: deny-all before any grant"
CAP_EMPTY="$(dejima cap list "$ISLAND" 2>&1)"
assert_has "$CAP_EMPTY" "deny-all" "no grant → deny-all (no host actions reachable)"
step "Capability: grant a target, see it listed, then revoke"
expect_ok "cap grant" dejima cap grant "$ISLAND" script
CAP_LS="$(dejima cap list "$ISLAND" 2>&1)"
assert_has "$CAP_LS" "script" "granted capability appears in the list"
expect_ok "cap revoke" dejima cap revoke "$ISLAND" script
CAP_GONE="$(dejima cap list "$ISLAND" 2>&1)"
assert_has "$CAP_GONE" "deny-all" "revoke returns to deny-all"

# ---------------------------------------------------------------------------
# Provider credentials — set / list (masked) / remove. The key is never echoed
# back; only metadata (provider name) is listed.
# ---------------------------------------------------------------------------
feature "provider credentials (set/list-masked/rm)"
step "Provider: none configured initially"
PROV_EMPTY="$(dejima provider ls 2>&1)"
assert_has "$PROV_EMPTY" "no providers configured" "fresh daemon has no provider creds"
step "Provider: set, list (masked), and remove"
expect_ok "provider set" dejima provider set anthropic --key "sk-itest-do-not-use"
PROV_LS="$(dejima provider ls 2>&1)"
assert_has "$PROV_LS" "anthropic" "set provider is listed"
if printf '%s' "$PROV_LS" | grep -qF "sk-itest-do-not-use"; then
  fail "provider ls leaked the raw key"
else
  pass "provider ls does not leak the raw key (masked)"
fi
expect_ok "provider rm" dejima provider rm anthropic

# ---------------------------------------------------------------------------
# Team tokens — mint an operator + a viewer token and confirm they're minted and
# listed. Role *enforcement* (a viewer denied an operate/owner action) is NOT
# exercised here, and deliberately so: this suite drives the daemon over the
# local unix socket, which is filesystem-trusted and runs as OWNER. The CLI does
# not forward DEJIMA_TOKEN over the socket (it attenuates only the TCP/in-island
# path — see clientForHost), so a token set here is ignored and every call runs
# as owner — an `expect_fail "viewer denied …"` would test nothing. Role
# attenuation is owned by internal/api/roleauth_test.go, which drives the HTTP
# surface with real role tokens (operator/viewer denied capOwner/capOperate
# routes incl. purge; owner allowed).
# ---------------------------------------------------------------------------
feature "team tokens (mint + list)"
step "Token: create an operator + a viewer token"
expect_ok "token create (operator)" dejima token create --role operator --label itest-op
VIEWER_OUT="$(dejima token create --role viewer --label itest-viewer 2>&1)"
assert_has "$VIEWER_OUT" "viewer" "viewer token minted"
TOK_LS="$(dejima token ls 2>&1)"
assert_has "$TOK_LS" "viewer" "minted tokens are listed"

# ---------------------------------------------------------------------------
# Webhooks — subscribe to events, see the subscription listed, unsubscribe. The
# event-type catalog is enumerable via `webhook events`.
# ---------------------------------------------------------------------------
feature "webhooks (events catalog/subscribe/ls/rm)"
step "Webhook: the event-type catalog is non-empty"
WH_EVENTS="$(dejima webhook events 2>&1)"
if [ -n "$(printf '%s' "$WH_EVENTS" | tr -d '[:space:]')" ]; then
  pass "webhook events lists known event types"
else
  fail "webhook events returned nothing"
fi
step "Webhook: subscribe → ls → rm"
WH_SUB="$(dejima webhook subscribe --url https://itest.invalid/hook 2>&1)"
assert_has "$WH_SUB" "subscribed" "subscription created"
WH_ID="$(printf '%s\n' "$WH_SUB" | sed -n 's/^subscribed:[[:space:]]*\([^ ]*\).*/\1/p' | head -1)"
WH_LS="$(dejima webhook ls 2>&1)"
assert_has "$WH_LS" "itest.invalid" "subscription appears in the list"
if [ -n "$WH_ID" ]; then expect_ok "webhook rm" dejima webhook rm "$WH_ID"; else fail "could not parse the webhook id"; fi

# ---------------------------------------------------------------------------
# Activity feed — the curated who-did-what timeline. After all the brokered ops
# above, the feed (or its always-on broker records) is reachable and exits 0.
# ---------------------------------------------------------------------------
feature "activity feed"
step "Activity: the curated feed is reachable"
expect_ok "activity feed exits 0" dejima activity

# ---------------------------------------------------------------------------
# Panic brake — engage stops every island and blocks auto-restart; status
# reports it; clear restarts. Exercised last (before purge guard's own island)
# so it doesn't interfere with the lifecycle assertions above.
# ---------------------------------------------------------------------------
feature "panic emergency brake (engage/status/clear)"
step "Panic: status is clear, engage, status engaged, clear"
PANIC0="$(dejima panic --status 2>&1)"
assert_has "$PANIC0" "not engaged" "panic starts disengaged"
expect_ok "panic engage" dejima panic --reason itest-drill
PANIC1="$(dejima panic --status 2>&1)"
assert_has "$PANIC1" "ENGAGED" "panic reports engaged after engaging"
expect_ok "panic clear" dejima panic --clear

# ---------------------------------------------------------------------------
# Doctor — the host/daemon health check runs and reports. It's diagnostic only
# (no --fix here), so it must exit 0 against a healthy isolated daemon.
# ---------------------------------------------------------------------------
feature "doctor health check"
step "Doctor: diagnostic run reports health"
DOC="$(dejima doctor 2>&1)"
if [ -n "$(printf '%s' "$DOC" | tr -d '[:space:]')" ]; then
  pass "doctor produced a health report"
else
  fail "doctor produced no output"
fi

# ---------------------------------------------------------------------------
# Uninstall --keep-islands re-adoption — the Lane B acceptance bar. Prove that
# `dejima uninstall --keep-islands` removes the daemon + binaries but KEEPS the
# island's named volume + ~/.dejima config, and that a *fresh install*
# (rebuild + restart daemon) re-adopts the pre-existing island by name with its
# workspace data intact. Runs LAST: it tears the daemon + binaries down, then
# stands a fresh one up — so it must follow every other feature.
# ---------------------------------------------------------------------------
feature "uninstall --keep-islands + re-adopt (Lane B acceptance bar)"

step "Re-adopt: create a dedicated island and write a workspace marker"
dejima init --name "$ISLAND_READOPT" --repo "$REPO" --local-copy --agent headless --cmd "sleep infinity" \
  >/dev/null 2>&1 || die "re-adopt island create failed (see $TMP/dejimad.log)"
dejima exec "$ISLAND_READOPT" -- sh -c 'echo readopt-survives > /workspace/keep.txt' >/dev/null 2>&1 \
  || die "could not write re-adopt marker"
WS_VOL="dejima-$ISLAND_READOPT-workspace"
CFG="$HOME/.dejima/projects/$ISLAND_READOPT/config.toml"
expect_ok "workspace volume exists before uninstall" docker volume inspect "$WS_VOL"
if [ -f "$CFG" ]; then pass "island config exists before uninstall"; else fail "island config missing before uninstall"; fi

step "Bare uninstall refuses without an explicit choice (no destructive default)"
expect_err_match "bare uninstall refuses" "explicit choice" dejima uninstall --yes
# The refusal must NOT have touched anything.
expect_ok "volume still present after refusal" docker volume inspect "$WS_VOL"

step "uninstall --keep-islands: remove daemon + binaries, KEEP volume + config"
# This stops the island container, uninstalls the (no-op in-test) service, and
# removes the test binaries — but must leave the named volume + ~/.dejima alone.
dejima uninstall --keep-islands --yes >"$TMP/uninstall.log" 2>&1 || true
# The foreground test daemon isn't a real service, so kill it explicitly to
# emulate the service stop that --keep-islands performs in production.
[ -n "$DAEMON_PID" ] && kill "$DAEMON_PID" >/dev/null 2>&1; wait "$DAEMON_PID" 2>/dev/null; DAEMON_PID=""
expect_ok "named volume survived --keep-islands" docker volume inspect "$WS_VOL"
if [ -f "$CFG" ]; then pass "the .dejima config survived --keep-islands"; else fail "config was deleted by --keep-islands (it must be kept)"; fi
# Prove the *data* in the kept volume is intact, independent of any container.
# Bounded: this throwaway `docker run` (the suite's only direct one) has wedged
# the whole nightly for 30+ min on a stalled colima engine. Cap it so a hung read
# fails one assertion fast — and the suite still finishes and reports — instead of
# hanging the job to its timeout. Force-remove the named container if the read
# times out (the killed client can't fire --rm).
VOLDATA="$(run_bounded 60 docker run --rm --name dejima-itest-voldata -v "$WS_VOL":/ws:ro dejima/island:latest sh -c 'cat /ws/keep.txt' 2>/dev/null)"; voldata_rc=$?
if [ "$voldata_rc" -eq 124 ]; then
  docker rm -f dejima-itest-voldata >/dev/null 2>&1 || true
  fail "reading the kept volume timed out after 60s (docker run wedged — engine stall)"
else
  assert_eq "$VOLDATA" "readopt-survives" "workspace data persists in the kept volume"
fi

step "Fresh install re-adopts: rebuild binaries, restart daemon, re-create island"
with_progress "recompiling dejima + dejimad" build_dejima_binaries || die "reinstall build failed"
dejimad --foreground >>"$TMP/dejimad.log" 2>&1 &
DAEMON_PID=$!
wait_for_daemon "restarting dejimad" || die "daemon socket never reappeared after reinstall"
# The kept config means the daemon already knows this island; re-creating it
# binds the SAME named volume (deterministic name) — re-adoption.
READOPT_LS="$(dejima ls 2>&1)"
if printf '%s' "$READOPT_LS" | grep -qF -- "$ISLAND_READOPT"; then
  pass "fresh daemon already lists the kept island (config re-adopted)"
else
  # Config-less daemons re-adopt on (re)create; bind the same volume by name.
  dejima init --name "$ISLAND_READOPT" --repo "$REPO" --local-copy --agent headless --cmd "sleep infinity" \
    >/dev/null 2>&1 || die "re-create after reinstall failed"
fi
# Wake/recreate the container so it mounts the pre-existing volume, then confirm
# the workspace marker written BEFORE the uninstall is still there.
dejima wake "$ISLAND_READOPT" >/dev/null 2>&1 || true
readopted=""
for _ in $(seq 1 30); do
  if dejima exec "$ISLAND_READOPT" -- test -f /workspace/keep.txt >/dev/null 2>&1; then readopted=1; break; fi
  sleep 1
done
if [ -n "$readopted" ]; then
  GOT="$(dejima exec "$ISLAND_READOPT" -- cat /workspace/keep.txt 2>/dev/null)"
  assert_eq "$GOT" "readopt-survives" "re-adopted island's workspace survived the uninstall→reinstall round-trip"
else
  fail "island never became exec-able after re-adoption"
fi

# ---------------------------------------------------------------------------
# Print the per-feature rollup and (when DEJIMA_REPORT is set) write the JSON
# summary as a CI artifact. Exits non-zero if any feature failed.
report_summary || { printf '\033[31mINTEGRATION TEST FAILED\033[0m\n'; exit 1; }
printf '\033[32mALL GREEN\033[0m\n'
