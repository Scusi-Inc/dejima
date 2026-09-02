# @dejima/sdk — TypeScript/JavaScript client

Thin client for the [Dejima](https://dejima.tech/) API: run a fleet of
AI coding agents on hardware you own. Mirrors the
[Python client](https://pypi.org/project/dejima-sdk/).

> **Alpha (0.x).** The API is stable in shape (`v1/`-prefixed) but fields may
> change until `1.0`. The REST layer mirrors
> [`openapi.yaml`](https://github.com/aoos/dejima/blob/master/openapi.yaml); the
> only hand-written piece is the PTY `Session` helper.

## Install

```bash
npm install @dejima/sdk
# attach() uses the global WebSocket (Node 22+/browser) when present,
# else falls back to the optional `ws` package:
npm install ws        # only for Node < 22 with token auth
```

Requires Node 18+ (uses the global `fetch` and `AbortController`). ESM-only.

## Quickstart

```ts
import { Client } from "@dejima/sdk";

// host/token from $DEJIMA_HOST and $DEJIMA_TOKEN, or pass explicitly:
const dj = new Client({ host: "100.101.102.103:7273" });

const isl = await dj.createIsland("git@github.com:you/foo.git", { agent: "claude-code" });
console.log(isl.name, isl.state);

await dj.addAgent(isl.name, { type: "codex" });   // second agent, own worktree

const out = await dj.exec(isl.name, ["git", "status", "--short"]);
console.log(out.stdout, "exit", out.exit_code);

for (const i of await dj.listIslands()) {
  console.log(i.name, i.state, (i.agents ?? []).length, "agents");
}

console.log(await dj.overview());  // daemon health, VM memory, rollup
```

## Interactive sessions

`attach()` opens the multi-attach PTY stream. The daemon speaks a small
JSON-envelope protocol over the WebSocket — `Session` hides that, so you deal in
raw `Uint8Array`. Pull-based (`recv`) or event-based (`onData`):

```ts
const s = await dj.attach(isl.name);     // or { agent: "p2" }
s.resize(40, 120);
s.sendText("ls -la\n");

// pull style
let chunk: Uint8Array | null;
while ((chunk = await s.recv()) !== null) {
  process.stdout.write(chunk);
}

// or event style
s.onData((bytes) => process.stdout.write(bytes));
s.onClose(() => console.log("session ended"));
```

`dj.attachTerminal("t1")` does the same for an operator host terminal.

> Browser note: the global `WebSocket` cannot set request headers, so
> token-authenticated attach needs Node with the `ws` package (or a
> `wsFactory` you pass to `new Client(...)`). Operator (no-token) access works
> anywhere.

## API coverage

The client covers the full v1 surface: islands (list/create/get/update/delete,
hibernate/wake/reset/upgrade, clone, resources, workspace-ready, events), agents
(list/add/get/update/remove, configure), exec & files, Port broker, capability
broker, MCP broker (grants + `mcpCall`), credentials (Claude/GitHub/providers),
operator tokens (create/list/revoke), webhooks, team activity feed, daemon (overview/agent-types/
healthz/audit with filters + jsonl/csv export/clients/sessions-revoke/panic/
admin-update/image-build/ssh keys), host terminals, and the PTY `Session`.

Every non-2xx response rejects with a `DejimaError` (`.status`, `.description`).

## Auth

- **Operator** (unix socket / tailnet) needs no token.
- **Autonomy path** uses a per-island bearer token — set `DEJIMA_TOKEN` or pass
  `{ token }` to the constructor.

## Development

```bash
npm install
npm run build       # tsc -> dist/
npm test            # tsx --test (no daemon required)
```
