# Capability Broker — spec

**Status:** drafted 2026-06-15, ratified direction (see
[`capability-brokering.md`](capability-brokering.md)). Implements **Option C**
(narrow, typed capability adapters) **now**, not post-dogfood — frameworks like
OpenClaw / Hermes / Letta are function-calling brains that need *structured tool
calls*, not just a filesystem. **Option B (general host-command broker) is
permanently rejected** (`§3.4` ledger-tractability trap).

This spec defines the broker that lets a contained brain *do* a curated host
action — fire a macOS Shortcut, run a user-authored script — without ever
reaching a shell. It mirrors the Port (`port-island-spec.md`): deny-all default,
per-island explicit grant, fail-closed, every operation a typed Ledger entry.

---

## 1. Model

A **capability** is a *named, user-curated, host-side action* an island may be
granted permission to invoke with a structured argument map — nothing more. The
brain picks a **target by name** and passes **key→value args**; it can never name
a command, a path, or a shell string.

Three nouns, mirroring the Port:

| Port            | Capability broker        |
|-----------------|--------------------------|
| scope (a host dir grant) | **grant** (an island↦target permission) |
| trade (a file crossing)  | **execution** (one invocation of a target) |
| `trade.read/write` ledger entry | `capability.execute` ledger entry |

The curation lives **on the host, authored by the user** — that is the security
boundary. On macOS the curated set is the user's **Apple Shortcuts** (the user
defines what `MorningBrief` does); on Linux it is a strict directory of
**user-authored executables**, `~/.dejima/capabilities/`. The brain can only
invoke what the user already chose to expose, and only targets explicitly
granted to that island.

---

## 2. Adapters

An **adapter** maps a `(target, args)` request to a host action. V1 ships one
adapter per platform, selected by the daemon host OS. Adapters never interpolate
input into a shell; they `exec` a fixed program with a fixed argv and pass args
out-of-band (see §5).

### 2.1 macOS — Apple Shortcuts (`adapter=shortcuts`)
- **Target** = a Shortcut name. The user's Shortcuts library *is* the allowlist.
- **Invocation:** `exec("/usr/bin/shortcuts", "run", <target>, "--input-path", "-")`
  with the JSON-encoded args map on **stdin** (Shortcuts receives it as the
  shortcut input; the shortcut author parses it). Output is the shortcut's result
  on stdout, captured and returned.
- **Existence check:** target must appear in `shortcuts list` (cached, refreshed
  on miss) — an ungranted *or* nonexistent target fails closed.

### 2.2 Linux — capability scripts (`adapter=script`)
- **Target** = a basename in `~/.dejima/capabilities/` (no path separators).
- **Invocation:** `exec("~/.dejima/capabilities/<target>")` with the JSON args
  map on **stdin** and a minimal env (`DEJIMA_CAP_ISLAND`, `DEJIMA_CAP_AGENT`,
  `DEJIMA_CAP_TARGET`); no inherited shell, no args interpolation.
- **Trust gates** (all required, else fail closed): the file exists, is a regular
  file, is owned by the daemon user, is **not group/world-writable**, and has the
  execute bit. This makes "drop a script in the dir" the deliberate curation act,
  and stops a compromised island (which cannot write there) from minting targets.

> The two adapters share one wire contract (§4) and one ledger schema (§6); only
> the host-side mapping differs. A future adapter (e.g. `notify`) is another
> entry in the registry, reviewed on its own — never a generalization of these.

---

## 3. Grant surface (operator)

Mirrors `dejima port`. Grants live host-side in the island's
`~/.dejima/projects/<island>/config.toml` (`[[capabilities]]`, alongside
`[[ports]]` — outside any container), deny-all default, every grant/revoke
ledgered.

```
dejima cap grant  <island> <target>     # allow island to invoke target
dejima cap revoke <island> <target>     # remove the grant
dejima cap ls     <island>              # list this island's granted targets
dejima cap ls                           # all grants, all islands
```

- `<target>` is validated against the host's available set (warn if a granted
  Shortcut/script doesn't currently exist — grant is recorded but will fail
  closed at execution until it does).
- Granting is **always the operator** over the trusted control plane (unix socket
  / tailnet TCP). A token-authenticated in-island caller can **never** grant —
  same rule as Port scopes (`tokenauth.go`: grant routes are `accessDeny`).

---

## 4. API

### `POST /v1/capabilities/execute`
The single execution endpoint. Body:

```json
{ "target": "MorningBrief", "args": { "topic": "infra", "limit": "5" } }
```

- `target` (string, required) — the granted capability name.
- `args` (object of string→string, optional) — structured arguments. String
  values only in V1 (keeps the wire + ledger trivially typed; the adapter/shortcut
  parses richer shapes if it wants).

Response `200`:
```json
{ "ok": true, "output": "…stdout…", "exit_code": 0, "ledger_seq": 421 }
```
Errors: `401` (bad/missing token), `403` (target not granted to this island),
`404` (target not found on host), `409`/`422` (adapter/exec failure), `504`
(timeout). Every outcome — allowed or denied — is ledgered (§6).

**Island resolution.** For a token-authenticated caller (the in-island brain) the
island is taken from the **bearer token** (`TokenIslandFromContext`), so the
brain calls a fixed URL and cannot target another island. For an operator caller
(unix socket / tailnet) the island is a required `"island"` body field.

> Path note: the route is deliberately *not* `/v1/islands/{name}/…`. The token
> already pins the island, so a name-in-path adds nothing and invites the
> encoded-slash class of bug that `tokenauth.go` had to defend against. The
> authorization is "valid token + target granted to the token's island."

---

## 5. Authn / authz / injection safety

1. **Transport & authn** — same path as autonomy (#8): the in-island brain
   reaches `dejimad` over the host-internal `--token-tcp` listener with its
   per-island bearer token; constant-time auth in `porttoken`.
2. **tokenauth access class** — add `POST /v1/capabilities/execute` to
   `tokenRouteAccess` with a new class **`accessTokenOwn`**: reachable by *any*
   valid token, scoped by the handler to the token's own island. (Distinct from
   `accessOwnIsland`, which parses `{name}` from the path — there is none here —
   and from `accessAny`, which is the data-free probe.)
3. **Authorization** — the handler looks up the island's grants and requires an
   exact `target` match. Deny-all default; no grant ⇒ `403`, ledgered as
   `capability.deny`.
4. **No shell, ever** — adapters `exec` a fixed program with a fixed argv. The
   `target` is matched against the grant list (exact string), never passed to a
   shell; `args` travel as a JSON object on stdin, never interpolated. There is no
   code path from request text to a shell, so there is nothing to inject into.
5. **Argument bounds** — cap arg count, key/value length, and total payload;
   reject non-string values in V1. Execution has a wall-clock **timeout** and
   captured-output size cap (fail closed on exceed).

---

## 6. Ledger

Reuse the hash-chained host-side Ledger (`internal/ledger`, `~/.dejima/
ledger.jsonl`) — the same substrate as Port, so capabilities are auditable by the
same `dejima audit [--verify]`. New entry types, fixed schema:

| Type                 | When                          |
|----------------------|-------------------------------|
| `capability.grant`   | operator grants island↦target |
| `capability.revoke`  | operator revokes              |
| `capability.execute` | a granted invocation ran      |
| `capability.deny`    | an invocation was refused (ungranted / not found / bounds) |

Per-execution fields (mapped onto the existing `Entry`):

```
{ type, seq, time, prev, chain,        // chain — unchanged
  island, agent,                        // who
  target,                               // capability name  (Entry.Scope)
  adapter,                              // "shortcuts" | "script"  (Entry.Detail)
  args_sha256,                          // hash of canonical args  (Entry.SHA256)
  decision,                             // "allowed" | "denied"
  exit_code }                           // adapter exit (executes only)
```

**Args are hashed, not stored**, by default — same posture as file trades storing
a content hash, not contents: it keeps the ledger tractable and PII-free while
still proving *which* args produced an effect (the caller can reproduce + verify
the hash). A future opt-in `--record-args` can store the raw map for a target the
operator marks non-sensitive.

This is exactly why **B was rejected**: an arbitrary-command broker logs an argv
whose *effect* is unknowable; a typed capability logs a fixed schema whose effect
is the named target's published behavior — tractable and complete.

---

## 7. Threat model (what a compromised brain can / cannot do)

- **Cannot** invoke any capability it wasn't explicitly granted (deny-all).
- **Cannot** invoke another island's grants (island pinned by token).
- **Cannot** create or alter targets: Shortcuts live in the user's library;
  Linux scripts live in a daemon-user-owned, non-island-writable dir with strict
  mode checks. The island has no write path to either.
- **Cannot** reach a shell or arbitrary argv — only a fixed adapter program with
  a JSON args payload.
- **Can** invoke a *granted* target with attacker-chosen args. The blast radius
  is therefore *exactly the published behavior of what the user curated* — the
  same trust the user already extends by authoring that Shortcut/script. Grant
  narrowly; treat a capability target like a small, audited API.
- **Residual:** a capability target that itself does something dangerous with its
  args is the user's authoring responsibility (documented in the `cap grant`
  warning), just as a Shortcut the user builds can do anything they let it.

---

## 8. Out of scope (V1)

- **General command broker (B)** — permanently rejected; record and reject any
  pressure toward `port run <cmd>` here.
- **Non-string arg values, streaming/long-running targets, capability discovery
  from inside the island** (the brain is told its grants out-of-band by the
  operator/wrapper). Candidates for later, each reviewed on its own.

---

## 9. Implementation phasing

1. **Storage + grants** — ✅ done. Per-island grants in `config.toml`
   (`internal/project/capabilities.go`), operator-only routes
   `…/capability/grants` (`internal/api/capability.go`), `dejima cap
   grant/revoke/ls` (`cmd/dejima/capability.go`), `capability.grant/revoke`
   ledger entries. No execution yet — a grant is a recorded, ledgered permission.
2. **Adapter registry + Linux `script` adapter** — fully testable in CI without
   macOS (exec a temp script dir; assert stdin JSON, env, mode gates, timeout).
3. **`POST /v1/capabilities/execute` + `accessTokenOwn`** in `tokenauth.go`;
   `capability.execute/deny` ledgering; bounds + timeout.
4. **macOS `shortcuts` adapter** — behind host-OS selection; live-verified on
   Minion (mirrors how #8/OpenClaw were validated).
5. **Docs** — fold the resolved decision into `port-island-spec.md §3.4/§10.4`;
   add a brain-facing "calling a capability" note to the runbook.

Each phase lands behind the broker being **off unless at least one grant exists**
(no grants ⇒ the endpoint is reachable but every call is a `403` deny), so the
files-only default is preserved for anyone who doesn't opt in.
