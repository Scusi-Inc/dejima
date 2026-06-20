# MCP Broker — spec

**Status:** built 2026-06-20 (build-queue #3 — "Audited MCP brokering"). Implements
the deny-by-default, per-island, ledgered brokering of **MCP (Model Context
Protocol) servers** into an island. It is the **same pattern** as the Port
(`port-island-spec.md`) and the capability broker (`capability-broker-spec.md`),
applied to MCP servers: deny-all default, per-island explicit grant, fail-closed,
every operation a typed Ledger entry.

MCP is the default agent tool layer (Anthropic CMA and most platforms connect to
it), so brokering it is **parity** (table stakes) *and* a **differentiator**
(nobody audits MCP access). This spec mirrors the capability broker deliberately:
read that one first — the nouns and the threat model carry over verbatim.

---

## 1. Model

An **MCP server** is a *named, user-curated, host-side program* (a stdio MCP
server) an island may be granted permission to reach. The agent picks a **server
by name** and a **method** from a fixed brokered surface, and passes JSON-RPC
**params**; it can never name a command, a path, or a transport.

Three nouns, mirroring the Port and the capability broker:

| Port            | Capability broker | MCP broker |
|-----------------|-------------------|------------|
| scope (a host dir grant) | grant (island↦target) | **grant** (island↦server-name) |
| trade (a file crossing)  | execution (one invocation) | **call** (one JSON-RPC call) |
| `trade.read/write`       | `capability.execute`       | `mcp.call` ledger entry |

The curation lives **on the host, authored by the user** — that is the security
boundary. The user authors `~/.dejima/mcp/servers.toml` (which the island cannot
write), naming each server's program + argv. The agent can only invoke a server
the user already chose to expose, and only one explicitly granted to that island.

---

## 2. The host registry (curation boundary)

`~/.dejima/mcp/servers.toml`, owned by the daemon user, not group/world-writable
(checked at read time — a failed check is `ErrServerUntrusted`, fail closed):

```toml
[[servers]]
name = "files"                 # the handle a grant + a call address
transport = "stdio"            # only stdio in V1 (no network surface)
command = "/usr/local/bin/mcp-files"
args = ["--root", "/data/notes"]
env = ["MCP_FILES_TOKEN=…"]    # explicit passthrough; daemon env is NOT inherited
```

- The registry is read **fresh on every call**, so an operator edit takes effect
  with no daemon restart (like the capability script adapter re-`Lstat`ing its
  target each invocation).
- A **missing** registry is deny-all (nothing to invoke), not an error.
- The server program is the operator's curation; the broker execs it with a fixed
  argv and a minimal env (`DEJIMA_MCP_ISLAND/AGENT/SERVER`, a fixed `PATH`, plus
  the declared `env`) — never through a shell, never inheriting the daemon's env.

> A future transport (HTTP/SSE) is a sibling broker selected the same way, never a
> generalization of the stdio one — same posture as the capability adapters.

---

## 3. Grant surface (operator)

Mirrors `dejima cap`. Grants live host-side, deny-all default, every grant/revoke
ledgered. **They live in a per-island sidecar `~/.dejima/projects/<island>/
mcp.toml`** — deliberately *not* a field on `project.Project` — so this lane and
the team-auth lane can extend an island's config without touching the same struct.

```
dejima mcp grant  <island> <server>   # allow island to invoke a named server
dejima mcp revoke <island> <server>   # remove the grant
dejima mcp ls     <island>            # list this island's granted servers
dejima mcp call   <island> <server> --method tools/list
dejima mcp call   <island> <server> --method tools/call --params '{"name":"…","arguments":{…}}'
```

- The server need not be in the registry at grant time — a grant for a
  not-yet-registered server is recorded and fails closed at call time until the
  operator adds it (grant-ahead, like capabilities).
- Granting is **always the operator** over the trusted control plane (unix socket
  / tailnet TCP). A token-authenticated in-island caller can **never** grant —
  the grant routes are absent from `tokenauth.go`'s allow-list (default-deny).

---

## 4. API

### Grant routes (operator-only)
```
GET    /v1/islands/{name}/mcp/grants
POST   /v1/islands/{name}/mcp/grants          { "server": "files" }
DELETE /v1/islands/{name}/mcp/grants/{server}
```

### `POST /v1/mcp/call` — the brokered call
```json
{ "island": "oc-home", "server": "files",
  "method": "tools/call", "params": { "name": "read", "arguments": { "path": "x" } } }
```
- `server` (required) — a granted server name.
- `method` (required) — a member of the **brokered surface**: `tools/list`,
  `tools/call`, `resources/list`, `resources/read`, `prompts/list`, `prompts/get`.
  The lifecycle/handshake methods (`initialize`, `notifications/*`) are owned by
  the broker and are **never** callable.
- `params` (optional) — the JSON-RPC params (bounded, 64 KiB).

Response `200`:
```json
{ "ok": true, "is_error": false, "result": { … }, "ledger_seq": 42 }
```
`ok` is protocol success; `is_error` reflects a `tools/call` that *completed* with
`isError:true` (the call ran — the tool reported a problem; the caller's concern,
not a broker error, exactly like a capability's non-zero exit). Errors: `400`
(bad/oversized/disallowed-method), `403` (server not granted / registry
untrusted), `404` (island or server-in-registry not found), `502` (server spoke
malformed JSON-RPC / closed early), `503` (host can't broker MCP), `504`
(timeout). Every outcome — allowed or denied — is ledgered (§6).

**Island resolution.** The handler takes the island from the **bearer token**
when present (`TokenIslandFromContext`) and otherwise from the body `island`
field — so the operator/control-plane path works today, and the in-island token
path is correct the moment it is wired (§5).

---

## 5. Authn / authz / injection safety

1. **Deny-all grant check** — the handler loads the island's grants and requires
   an exact `server` match before the broker is ever reached; no grant ⇒ `403`,
   ledgered `mcp.deny`.
2. **Brokered method surface** — `method` must be in the fixed allow-list; a
   lifecycle/unknown method is `400` (ledgered deny), never forwarded.
3. **No shell, ever** — the broker execs the registry-named program with a fixed
   argv; `params` travel as JSON-RPC on the server's stdin, never interpolated.
   There is no path from request text to a shell.
4. **Bounds** — params payload, captured result size, and a wall-clock timeout are
   all capped; a hung server can't block the daemon (`WaitDelay` force-closes the
   pipes after the deadline).

**Token-path status (follow-up, owned by the team-auth lane).** In V1 the call
route is reached over the **trusted control plane** (operator / a wrapper holding
a service token), with the island named in the body. Making the route reachable
by an **in-island** bearer token — so a contained brain calls a fixed URL pinned
to its own island — is a **one-line addition** to `tokenauth.go`'s
`tokenRouteAccess` map: `"POST /v1/mcp/call": accessTokenOwn`. That file is owned
by the team-auth lane, so the wiring lands there; the handler already pins the
island from the token, so no handler change is needed. The grant routes stay
operator-only regardless.

---

## 6. Ledger

Reuse the hash-chained host-side Ledger (`internal/ledger`, `~/.dejima/
ledger.jsonl`) — the same substrate as Port and capabilities, so MCP access is
auditable by the same `dejima audit [--verify]`. New entry types, fixed schema:

| Type         | When                                   |
|--------------|----------------------------------------|
| `mcp.grant`  | operator grants island↦server          |
| `mcp.revoke` | operator revokes                       |
| `mcp.call`   | a granted call ran                     |
| `mcp.deny`   | a call was refused (ungranted / disallowed method / not in registry / protocol/timeout) |

Per-call fields (mapped onto the existing `Entry`):

```
{ type, seq, time, prev, chain,   // chain — unchanged
  island,                         // who
  scope,                          // server name
  detail,                         // method (+ "name=<tool>" for tools/call, + "isError")
  sha256,                         // canonical hash of the JSON-RPC params
  decision }                      // allowed | denied
```

**Params are hashed, not stored** (canonicalized by a JSON round-trip so key
order / whitespace don't change the hash) — same posture as a file trade's
content hash or a capability's args hash: tractable, PII-free, and still proves
*which* params produced an effect. This is exactly why a typed broker is
ledger-tractable where a general command broker is not.

---

## 7. Threat model (what a compromised brain can / cannot do)

- **Cannot** call any server it wasn't explicitly granted (deny-all).
- **Cannot** call another island's grants (island pinned by token, once wired;
  named-by-operator otherwise).
- **Cannot** create or alter servers: the registry is a daemon-user-owned,
  non-island-writable, trust-checked file. The island has no write path to it.
- **Cannot** reach a shell, an arbitrary program, or a lifecycle method — only a
  fixed registry program with a JSON-RPC payload, on the brokered method surface.
- **Can** call a *granted* server with attacker-chosen (bounded) params. The blast
  radius is *exactly the published behaviour of the MCP server the user curated* —
  the same trust the user already extends by authoring that server. Grant
  narrowly; treat a granted server like a small, audited API.

---

## 8. Out of scope (V1)

- **Non-stdio transports** (HTTP/SSE) — a sibling broker, reviewed on its own.
- **A full transparent MCP proxy** (the agent's MCP client pointed at dejima,
  every server multiplexed) — V1 brokers explicit `tools/call`/discovery via the
  API, which is the auditable surface; the transparent-proxy ergonomics are a
  later layer over this same grant + ledger substrate.
- **Capability/server discovery from inside the island** — the agent is told its
  grants out-of-band by the operator/wrapper.

---

## 9. For the SDK/OpenAPI lane (Lane 4)

These endpoints are new and should be added to `openapi.yaml`:
`GET|POST /v1/islands/{name}/mcp/grants`, `DELETE
/v1/islands/{name}/mcp/grants/{server}`, and `POST /v1/mcp/call`. Request/response
shapes are the `MCP*` types in `internal/api/mcp.go`
(`MCPGrantRequest/MCPGrantView/MCPGrantsResponse/MCPCallRequest/MCPCallResponse`).

---

## 10. Implementation map

- `internal/mcpbroker` — `Registry` (trust-checked `servers.toml`), the stdio
  JSON-RPC client (initialize → one brokered call → close), `StdioBroker`,
  `AllowedMethods`. Unit-tested against a re-exec mock MCP server.
- `internal/project/mcp.go` — per-island grant sidecar (deny-all default),
  separate file from `project.Project`.
- `internal/api/mcp.go` — grant/revoke/list handlers, the `POST /v1/mcp/call`
  broker handler, `mcp.*` ledger entries, `RegisterMCP(mux)` (the one shared seam
  in `server.go`), the API types, and the `Client` methods.
- `cmd/dejima/mcp.go` — `dejima mcp grant/ls/revoke/call`.
- `scripts/integration.sh` — a live broker section (mock stdio server → grant →
  `tools/list`/`tools/call` → `mcp.*` ledger + verify → revoke → deny-all).
