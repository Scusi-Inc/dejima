# dejima — Python client

Thin Python client for the [Dejima](https://aoos.github.io/dejima/) API: run a
fleet of AI coding agents on hardware you own.

> **Alpha (0.x).** The API is stable in shape (`v1/`-prefixed) but fields may
> change until `1.0`. This client is hand-written over the REST surface and
> mirrors [`openapi.yaml`](https://github.com/aoos/dejima/blob/master/openapi.yaml);
> the only non-generated piece is the PTY `Session` helper.

## Install

```bash
pip install dejima            # REST client
pip install 'dejima[ws]'      # + WebSocket PTY attach()
```

## Quickstart

```python
from dejima import Client

# host/token from $DEJIMA_HOST and $DEJIMA_TOKEN, or pass explicitly:
dj = Client(host="100.84.12.7:7273")

isl = dj.create_island(repo="git@github.com:you/foo.git", agent="claude-code")
print(isl["name"], isl["state"])

# add a second agent on its own worktree
dj.add_agent(isl["name"], type="codex")

# one-shot command
out = dj.exec(isl["name"], ["git", "status", "--short"])
print(out["stdout"], "exit", out["exit_code"])

# fleet view
for i in dj.list_islands():
    print(i["name"], i["state"], len(i.get("agents", [])), "agents")

print(dj.overview())   # daemon health, VM memory, rollup
```

## Interactive sessions

`attach()` opens the multi-attach PTY stream (needs the `ws` extra). The daemon
speaks a small JSON-envelope protocol over the WebSocket — `Session` hides that,
so you deal in raw `bytes`:

```python
with dj.attach(isl["name"]) as s:   # or agent="p2"
    s.resize(40, 120)
    s.send(b"ls -la\n")
    print(s.recv())                 # bytes of PTY output, or None when closed
```

`dj.attach_terminal("t1")` does the same for an operator host terminal.

## API coverage

The client covers the full v1 surface:

- **Islands** — list/create/get/update/delete, hibernate/wake/reset/upgrade,
  clone, resources, workspace-ready, events.
- **Agents** — list/add/get/update/remove, configure (provider/model).
- **Exec & files** — exec, file read/write, logs.
- **Port broker** — scopes (list/grant/revoke), intake/export/write.
- **Capability broker** — grants (list/grant/revoke), execute.
- **MCP broker** — grants (list/grant/revoke), `mcp_call`.
- **Credentials** — Claude push/status, GitHub identities + repos, LLM providers.
- **Operator tokens** — create/list/revoke (owner-only; role + island scope).
- **Events** — webhook subscribe/list/unsubscribe.
- **Activity** — team activity feed (`activity`, filterable).
- **Daemon** — overview, agent-types, healthz, audit (filters + jsonl/csv export),
  clients, sessions-revoke, panic, admin-update, image-build, SSH account keys.
- **Host terminals** — list/create/delete/relabel + `attach_terminal`.
- **Sessions** — `attach` / `attach_terminal` (PTY) and `session_url`.

Every non-2xx response raises `dejima.DejimaError` (`.status`, `.message`).

## Auth

- **Operator** (unix socket / tailnet) needs no token.
- **Autonomy path** (an agent driving its own/child islands) uses a per-island
  bearer token — set `DEJIMA_TOKEN` or pass `token=`.

## Development

```bash
pip install -e '.[dev]'
pytest
```

Tests run against a stdlib `http.server` stub — no running daemon required.
