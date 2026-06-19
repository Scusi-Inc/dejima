# dejima — Python client

Thin Python client for the [Dejima](https://aoos.github.io/dejima/) API: run a
fleet of AI coding agents on hardware you own.

> **Alpha (0.x).** The API is stable in shape (`v1/`-prefixed) but fields may
> change until `1.0`. This client is hand-written over the REST surface; once the
> API freezes, the request/response layer will be generated from
> [`openapi.yaml`](https://github.com/aoos/dejima/blob/master/openapi.yaml).

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

`attach()` opens the multi-attach PTY stream (needs the `ws` extra):

```python
ws = dj.attach(isl["name"])      # or agent="p2"
ws.send(b"ls -la\n")
print(ws.recv())
```

## API coverage

Islands (list/create/get/delete, hibernate/wake/reset, clone, resources),
agents (list/add/remove), exec, file read/write, logs, overview, agent-types,
healthz, webhook subscribe, and the PTY session URL/attach. Port broker,
capability broker, credentials, and terminal endpoints are reachable via the raw
session if needed — see [the API reference](https://aoos.github.io/dejima/api.html).

## Auth

- **Operator** (unix socket / tailnet) needs no token.
- **Autonomy path** (an agent driving its own/child islands) uses a per-island
  bearer token — set `DEJIMA_TOKEN` or pass `token=`.
