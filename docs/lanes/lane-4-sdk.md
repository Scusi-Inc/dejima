# Lane 4 — SDK & clients

You are the **SDK** agent for Dejima. Build the Python + TS SDKs and keep the
OpenAPI spec current — build-queue item #4. `pip install dejima`. **Fully
independent — no daemon code, safest lane, run flat out.**

**Read first (in order):**
- `openapi.yaml` (repo root) — the spec, your source of truth for the client.
- `sdk/python/` — the **starter client already exists**; extend it.
- `internal/api/server.go` (the route table) + `internal/api/types.go` (request/response shapes) — read-only, to keep the spec accurate. `api.html` (the human API reference).
- `docs/roadmap.md` → "Committed build queue" + "Parallel lanes".

**Scope, in order:**
1. **Flesh out `sdk/python`** — cover the remaining endpoints (port, capability, terminals, credentials, resources, agent config), add tests (mock or live daemon), finish packaging for PyPI (`pyproject.toml` is ready).
2. **Create `sdk/ts`** — a TypeScript client mirroring the Python one (incl. the WebSocket `attach`).
3. **Keep `openapi.yaml` in sync** with `server.go`; add an OpenAPI lint to CI; optionally generate the request/response layer from the spec (hand-keep only the PTY-stream helper).
4. **Update `api.html` / `build.html`** snippets to show `pip install dejima` usage.

**You own:** `openapi.yaml`, `sdk/`, the SDK snippets in `api.html` / `build.html`.
**Do NOT touch:** any `internal/` daemon Go code. You build *against* the API, not in it. If you find the API needs a change, **file it for Lane 1/2/3** — don't edit daemon code yourself.

**Gates / seams:** none — you have no dependency on the other lanes. Other lanes will ask you to spec new endpoints they add; fold those into `openapi.yaml` + the clients.

**Workflow:** branch `feat/lane4-sdk`, own island/worktree — **not `master`**. Run the Python tests; lint the spec. Commit your own hunks; PR to `master` when green. Ship with a clear "0.x — may change" note; the CLI (Go) is the reference client to mirror behavior.

**Done when:** Python + TS clients cover the API; `openapi.yaml` matches `server.go`; example snippets are live on the site; the Python package is publishable to PyPI.
