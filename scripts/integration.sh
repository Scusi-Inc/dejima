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
ISLAND_CLONE="itest-clone"
ISLAND_A="itest-a"
ISLAND_B="itest-b"
ISLAND_GUARD="itest-guard"
DAEMON_PID=""
# The --local-copy seed is bind-mounted into the Docker VM, so it must live on a
# path Docker shares. macOS shares /Users by default but NOT /var/folders (where
# mktemp lands) — so put the test seed repo under the real home.
REPO_DIR="$REAL_HOME/.cache/dejima-itest-$$"
REPO="$REPO_DIR/repo"

cleanup(){
  set +e
  for isl in "$ISLAND" "$ISLAND_MULTI" "$ISLAND_CLONE" "$ISLAND_A" "$ISLAND_B" "$ISLAND_GUARD"; do
    [ -n "$DAEMON_PID" ] && dejima purge "$isl" -f >/dev/null 2>&1
  done
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
# Lifecycle — list/status/exec, and the hibernate→wake→upgrade→reset verbs
# against the real container. These are the verbs the manual Minion pass walks;
# automating them here shrinks that queue.
# ---------------------------------------------------------------------------
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
step "Purge guard: unpushed work blocks a plain purge, --force overrides"
dejima init --name "$ISLAND_GUARD" --repo "$REPO" --local-copy --agent headless --cmd "sleep infinity" \
  >/dev/null 2>&1 || die "guard island create failed"
# Create an unpushed commit inside the island's workspace.
dejima exec "$ISLAND_GUARD" -- sh -c \
  'cd /workspace && git config user.email t@t && git config user.name t && echo work > unpushed.txt && git add -A && git commit -qm unpushed' \
  >/dev/null 2>&1 || die "could not create unpushed commit in the guard island"
expect_err_match "plain purge refused with unpushed work" "--force" \
  dejima purge "$ISLAND_GUARD"
expect_ok "force-purge overrides the guard" dejima purge "$ISLAND_GUARD" --force
expect_fail "guard island is gone after force-purge" dejima status "$ISLAND_GUARD"

# ---------------------------------------------------------------------------
printf '\n\033[1m──────── results ────────\033[0m\n'
printf 'passed: %d   failed: %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || { printf '\033[31mINTEGRATION TEST FAILED\033[0m\n'; exit 1; }
printf '\033[32mALL GREEN\033[0m\n'
